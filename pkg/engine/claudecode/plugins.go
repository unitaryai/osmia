package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/unitaryai/osmia/pkg/engine"
)

// PluginProfileLabelPrefix marks a ticket label that selects a plugin
// profile. A ticket labelled "osmia-plugins:build" loads the plugins of the
// "build" profile on top of the default plugins.
const PluginProfileLabelPrefix = "osmia-plugins:"

// pluginsEnvVar carries the plugins chosen for a task to setup-claude.sh as
// JSON. The script fetches each source at its pinned ref and passes every
// plugin directory to Claude Code as --plugin-dir.
const pluginsEnvVar = "OSMIA_PLUGINS"

// pluginTokenEnvPrefix prefixes the environment variable that holds a
// source's access token. setup-claude.sh unsets it once the source is
// fetched, so the agent never sees it.
const pluginTokenEnvPrefix = "OSMIA_PLUGIN_TOKEN_"

// defaultPluginTokenUsername is the HTTPS username sent with a source token.
// GitHub accepts any username with a personal access token and requires this
// one for an app installation token; GitLab accepts any non-blank username
// with a personal, project or group access token. Deploy tokens need their
// own username, set through PluginSource.TokenUsername.
const defaultPluginTokenUsername = "x-access-token"

var (
	pluginSourceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	pluginNameRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	pluginRefRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// PluginSource is a git repository that holds a Claude Code plugin
// marketplace (a .claude-plugin/marketplace.json at its root). It is fetched
// at a fixed ref so that a push to the repository cannot change what agents
// run until an operator moves the ref.
type PluginSource struct {
	// Name identifies the source in plugin references ("<source>/<plugin>").
	Name string
	// RepoURL is the HTTPS clone URL of the repository.
	RepoURL string
	// Ref is the commit SHA or tag to fetch. A commit SHA is the safer choice:
	// a tag can be moved.
	Ref string
	// TokenSecret names a Kubernetes Secret holding a read token for a private
	// repository. Empty for a public repository.
	TokenSecret string
	// TokenKey is the key within TokenSecret. Defaults to "token".
	TokenKey string
	// TokenUsername is the HTTPS username sent with the token. Defaults to
	// "x-access-token". A GitLab deploy token needs its own username here.
	TokenUsername string
}

// PluginRef names one plugin in one source.
type PluginRef struct {
	// Source is the PluginSource name.
	Source string
	// Plugin is the plugin name as listed in the source's marketplace.json.
	Plugin string
}

// PluginConfig chooses which plugins each task loads.
type PluginConfig struct {
	// Sources lists the repositories plugins come from.
	Sources []PluginSource
	// Default lists plugins loaded on every task.
	Default []PluginRef
	// Profiles maps a profile name to the extra plugins, and optionally the
	// engine image, that a task gets when its ticket carries the label
	// "osmia-plugins:<profile>".
	Profiles map[string]PluginProfile
}

// PluginProfile is what a profile label adds to a task.
type PluginProfile struct {
	// Plugins are loaded on top of the defaults.
	Plugins []PluginRef
	// Image, when set, replaces the engine image for the task: a build
	// profile can run on an image with build tools while other tasks keep the
	// smaller default.
	Image string
	// Init, when set, is an executable in the image that setup-claude.sh runs
	// before Claude starts, for set-up the image needs at runtime, such as
	// creating writable folders under /workspace.
	Init string
	// Workdir, when set, is the directory in the image Claude starts in,
	// instead of /workspace. A tool whose skills expect to run from its own
	// checkout ships that checkout in its image and names it here.
	Workdir string
}

// ParsePluginRef parses a "<source>/<plugin>" reference.
func ParsePluginRef(s string) (PluginRef, error) {
	source, plugin, ok := strings.Cut(s, "/")
	if !ok || source == "" || plugin == "" {
		return PluginRef{}, fmt.Errorf("plugin reference %q must have the form <source>/<plugin>", s)
	}
	return PluginRef{Source: source, Plugin: plugin}, nil
}

// String returns the reference in "<source>/<plugin>" form.
func (r PluginRef) String() string {
	return r.Source + "/" + r.Plugin
}

// Validate checks that every source is well formed and that every plugin
// reference names a configured source. Names end up in environment variable
// names, file paths and shell arguments inside the agent pod, so they are
// restricted to safe characters.
func (c PluginConfig) Validate() error {
	sources, err := c.validateSources()
	if err != nil {
		return err
	}
	if err := validatePluginRefs("default", c.Default, sources); err != nil {
		return err
	}
	for name, profile := range c.Profiles {
		if !pluginSourceNameRe.MatchString(name) {
			return fmt.Errorf("plugin profile name %q must be lowercase letters, digits and hyphens", name)
		}
		if err := validatePluginRefs("profile "+name, profile.Plugins, sources); err != nil {
			return err
		}
		if profile.Image != "" && strings.IndexFunc(profile.Image, unicode.IsSpace) >= 0 {
			return fmt.Errorf("plugin profile %q: image %q contains whitespace", name, profile.Image)
		}
		if err := validateImagePath("init", profile.Init); err != nil {
			return fmt.Errorf("plugin profile %q: %w", name, err)
		}
		if err := validateImagePath("workdir", profile.Workdir); err != nil {
			return fmt.Errorf("plugin profile %q: %w", name, err)
		}
	}
	return nil
}

// validateSources checks each source and returns them keyed by name.
func (c PluginConfig) validateSources() (map[string]PluginSource, error) {
	sources := make(map[string]PluginSource, len(c.Sources))
	for _, s := range c.Sources {
		if !pluginSourceNameRe.MatchString(s.Name) {
			return nil, fmt.Errorf("plugin source name %q must be lowercase letters, digits and hyphens", s.Name)
		}
		if _, dup := sources[s.Name]; dup {
			return nil, fmt.Errorf("plugin source %q is defined more than once", s.Name)
		}
		if err := validatePluginRepoURL(s.RepoURL); err != nil {
			return nil, fmt.Errorf("plugin source %q: %w", s.Name, err)
		}
		if err := validatePluginRef(s.Ref); err != nil {
			return nil, fmt.Errorf("plugin source %q: %w", s.Name, err)
		}
		if s.TokenSecret == "" && (s.TokenKey != "" || s.TokenUsername != "") {
			return nil, fmt.Errorf("plugin source %q: token_key and token_username need token_secret", s.Name)
		}
		sources[s.Name] = s
	}
	return sources, nil
}

// validatePluginRef checks that a ref is a commit SHA or a tag name. The pod
// fetches anything that is not a full SHA as refs/tags/<ref>, so a branch
// name only works if a tag of that name exists; HEAD and fully qualified
// refs are rejected outright, since they would name a branch.
func validatePluginRef(ref string) error {
	switch {
	case !pluginRefRe.MatchString(ref) || strings.Contains(ref, ".."):
		return fmt.Errorf("ref must be a commit SHA or tag, got %q", ref)
	case ref == "HEAD" || strings.HasPrefix(ref, "refs/"):
		return fmt.Errorf("ref %q names a branch; use a commit SHA or tag", ref)
	}
	return nil
}

// validatePluginRepoURL checks that a source URL is an https:// URL with a
// host, no credentials, and no query or fragment. A token belongs in
// token_secret: a URL is copied into the pod environment and the logs, so a
// credential in its userinfo or query would leak. For the same reason no
// error here quotes the URL; url.Parse's error does, so it is not wrapped.
func validatePluginRepoURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("repo_url is not a valid URL")
	}
	if u.Scheme != "https" || u.Host == "" {
		return errors.New("repo_url must be an https:// URL with a host")
	}
	if u.User != nil {
		return errors.New("repo_url must not contain credentials; use token_secret")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("repo_url must not have a query or fragment; use token_secret for credentials")
	}
	return nil
}

// validateImagePath checks a path inside the agent image: empty, or absolute
// and clean, with no whitespace. It reaches setup-claude.sh through the
// environment, so it is kept to what a shell can use unquoted by mistake
// without harm.
func validateImagePath(field, p string) error {
	switch {
	case p == "":
		return nil
	case !path.IsAbs(p) || path.Clean(p) != p:
		return fmt.Errorf("%s %q must be a clean absolute path", field, p)
	case strings.IndexFunc(p, unicode.IsSpace) >= 0:
		return fmt.Errorf("%s %q contains whitespace", field, p)
	}
	return nil
}

// validatePluginRefs checks that each reference is well formed and names a
// configured source.
func validatePluginRefs(where string, refs []PluginRef, sources map[string]PluginSource) error {
	for _, r := range refs {
		if _, ok := sources[r.Source]; !ok {
			return fmt.Errorf("plugin %q in %s names an unknown source %q", r, where, r.Source)
		}
		if !pluginNameRe.MatchString(r.Plugin) {
			return fmt.Errorf("plugin %q in %s has an invalid plugin name", r, where)
		}
	}
	return nil
}

// WithPlugins sets the plugin sources and the rules that choose which
// plugins each task loads. The config must already have passed Validate.
func WithPlugins(cfg PluginConfig) Option {
	return func(e *ClaudeCodeEngine) {
		e.plugins = cfg
	}
}

// taskSelection is what a task's labels choose: its plugins and the
// single-valued settings a profile can set.
type taskSelection struct {
	plugins []PluginRef
	image   string
	init    string
	workdir string
}

// selectionForTask returns the plugins a task loads, the defaults then those
// of each profile its labels select without duplicates, and the image, init
// script and working directory the selected profiles set, if any.
//
// A label naming an unknown profile is an error, so a typo fails the task
// instead of silently running it without what it asked for. So are two
// selected profiles that set different values for the same setting, since
// only one can apply.
func (c PluginConfig) selectionForTask(labels []string) (taskSelection, error) {
	sel := taskSelection{plugins: slices.Clone(c.Default)}
	image, init, workdir := singleSetting{field: "image"}, singleSetting{field: "init"}, singleSetting{field: "workdir"}
	for _, label := range labels {
		name, ok := strings.CutPrefix(label, PluginProfileLabelPrefix)
		if !ok {
			continue
		}
		profile, known := c.Profiles[name]
		if !known {
			return taskSelection{}, fmt.Errorf("label %q names unknown plugin profile %q", label, name)
		}
		sel.plugins = append(sel.plugins, profile.Plugins...)
		for _, set := range []struct {
			setting *singleSetting
			value   string
		}{{&image, profile.Image}, {&init, profile.Init}, {&workdir, profile.Workdir}} {
			if err := set.setting.take(name, set.value); err != nil {
				return taskSelection{}, err
			}
		}
	}
	sel.plugins = dedupePluginRefs(sel.plugins)
	sel.image, sel.init, sel.workdir = image.value, init.value, workdir.value
	return sel, nil
}

// singleSetting collects a setting that only one value can apply to, across
// the profiles a task selects.
type singleSetting struct {
	field string
	value string
	from  string
}

// take records the value profile sets, failing when another profile set a
// different one. An empty value sets nothing.
func (s *singleSetting) take(profile, value string) error {
	if value == "" || value == s.value {
		return nil
	}
	if s.value != "" {
		return fmt.Errorf("plugin profiles %q and %q set different %ss; a task can have only one", s.from, profile, s.field)
	}
	s.value, s.from = value, profile
	return nil
}

// pluginSpec is the per-source entry in the OSMIA_PLUGINS JSON read by
// setup-claude.sh.
type pluginSpec struct {
	Source        string   `json:"source"`
	RepoURL       string   `json:"repo_url"`
	Ref           string   `json:"ref"`
	TokenEnv      string   `json:"token_env,omitempty"`
	TokenUsername string   `json:"token_username,omitempty"`
	Plugins       []string `json:"plugins"`
}

// pluginExecution is what a task needs in its pod to load its plugins, and
// the engine image and working directory a profile chose for it, if any.
type pluginExecution struct {
	env           map[string]string
	secretKeyRefs map[string]engine.SecretKeyRef
	image         string
	workdir       string
}

// Environment variables that tell setup-claude.sh to run an init script and
// where to start Claude.
const (
	initScriptEnvVar = "OSMIA_INIT_SCRIPT"
	workdirEnvVar    = "OSMIA_WORKDIR"
)

// pluginExecutionForTask works out the plugins a task loads and the
// environment and secrets the pod needs to fetch them. Only the sources the
// task uses are fetched, and only their tokens are mounted.
func (c PluginConfig) pluginExecutionForTask(labels []string) (pluginExecution, error) {
	sel, err := c.selectionForTask(labels)
	if err != nil {
		return pluginExecution{}, err
	}
	exec := pluginExecution{image: sel.image, workdir: sel.workdir, env: map[string]string{}}
	if sel.init != "" {
		exec.env[initScriptEnvVar] = sel.init
	}
	if sel.workdir != "" {
		exec.env[workdirEnvVar] = sel.workdir
	}
	if len(sel.plugins) == 0 {
		return exec, nil
	}
	specs, secretKeyRefs := c.pluginSpecs(sel.plugins)
	payload, err := json.Marshal(specs)
	if err != nil {
		return pluginExecution{}, fmt.Errorf("encoding plugin specs: %w", err)
	}
	exec.env[pluginsEnvVar] = string(payload)
	exec.secretKeyRefs = secretKeyRefs
	return exec, nil
}

// pluginSpecs groups plugin references by source, in the order sources are
// configured, and collects the token secret of each source used.
func (c PluginConfig) pluginSpecs(refs []PluginRef) ([]pluginSpec, map[string]engine.SecretKeyRef) {
	var specs []pluginSpec
	secretKeyRefs := make(map[string]engine.SecretKeyRef)
	for _, src := range c.Sources {
		var plugins []string
		for _, r := range refs {
			if r.Source == src.Name {
				plugins = append(plugins, r.Plugin)
			}
		}
		if len(plugins) == 0 {
			continue
		}
		spec := pluginSpec{Source: src.Name, RepoURL: src.RepoURL, Ref: src.Ref, Plugins: plugins}
		if src.TokenSecret != "" {
			spec.TokenEnv = pluginTokenEnvPrefix + toSafeEnvName(src.Name)
			spec.TokenUsername = src.tokenUsername()
			secretKeyRefs[spec.TokenEnv] = engine.SecretKeyRef{SecretName: src.TokenSecret, Key: src.tokenKey()}
		}
		specs = append(specs, spec)
	}
	return specs, secretKeyRefs
}

// tokenKey returns the Secret key holding the source token.
func (s PluginSource) tokenKey() string {
	if s.TokenKey != "" {
		return s.TokenKey
	}
	return "token"
}

// tokenUsername returns the HTTPS username sent with the source token.
func (s PluginSource) tokenUsername() string {
	if s.TokenUsername != "" {
		return s.TokenUsername
	}
	return defaultPluginTokenUsername
}

// dedupePluginRefs returns refs without repeats, keeping first-seen order.
func dedupePluginRefs(refs []PluginRef) []PluginRef {
	seen := make(map[PluginRef]struct{}, len(refs))
	out := refs[:0]
	for _, r := range refs {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
