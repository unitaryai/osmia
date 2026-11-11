package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

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
	// Profiles maps a profile name to extra plugins. A task gets a profile's
	// plugins when its ticket carries the label "osmia-plugins:<profile>".
	Profiles map[string][]PluginRef
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
	for name, refs := range c.Profiles {
		if !pluginSourceNameRe.MatchString(name) {
			return fmt.Errorf("plugin profile name %q must be lowercase letters, digits and hyphens", name)
		}
		if err := validatePluginRefs("profile "+name, refs, sources); err != nil {
			return err
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
		if !pluginRefRe.MatchString(s.Ref) || strings.Contains(s.Ref, "..") {
			return nil, fmt.Errorf("plugin source %q: ref must be a commit SHA or tag, got %q", s.Name, s.Ref)
		}
		if s.TokenSecret == "" && (s.TokenKey != "" || s.TokenUsername != "") {
			return nil, fmt.Errorf("plugin source %q: token_key and token_username need token_secret", s.Name)
		}
		sources[s.Name] = s
	}
	return sources, nil
}

// validatePluginRepoURL checks that a source URL is an https:// URL with a
// host and no credentials. A token belongs in token_secret: a URL is copied
// into the pod environment and the logs, so credentials in it would leak.
func validatePluginRepoURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("repo_url is not a valid URL: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return errors.New("repo_url must be an https:// URL with a host")
	}
	if u.User != nil {
		return errors.New("repo_url must not contain credentials; use token_secret")
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

// pluginsForTask returns the plugins a task loads: the defaults, then the
// plugins of each profile its labels select, without duplicates. A label
// naming an unknown profile is an error, so a typo fails the task instead of
// silently running it without the plugins it asked for.
func (c PluginConfig) pluginsForTask(labels []string) ([]PluginRef, error) {
	refs := slices.Clone(c.Default)
	for _, label := range labels {
		profile, ok := strings.CutPrefix(label, PluginProfileLabelPrefix)
		if !ok {
			continue
		}
		profileRefs, known := c.Profiles[profile]
		if !known {
			return nil, fmt.Errorf("label %q names unknown plugin profile %q", label, profile)
		}
		refs = append(refs, profileRefs...)
	}
	return dedupePluginRefs(refs), nil
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

// pluginExecution is what a task needs in its pod to load its plugins.
type pluginExecution struct {
	env           map[string]string
	secretKeyRefs map[string]engine.SecretKeyRef
}

// pluginExecutionForTask works out the plugins a task loads and the
// environment and secrets the pod needs to fetch them. Only the sources the
// task uses are fetched, and only their tokens are mounted.
func (c PluginConfig) pluginExecutionForTask(labels []string) (pluginExecution, error) {
	refs, err := c.pluginsForTask(labels)
	if err != nil || len(refs) == 0 {
		return pluginExecution{}, err
	}
	specs, secretKeyRefs := c.pluginSpecs(refs)
	payload, err := json.Marshal(specs)
	if err != nil {
		return pluginExecution{}, fmt.Errorf("encoding plugin specs: %w", err)
	}
	return pluginExecution{
		env:           map[string]string{pluginsEnvVar: string(payload)},
		secretKeyRefs: secretKeyRefs,
	}, nil
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
