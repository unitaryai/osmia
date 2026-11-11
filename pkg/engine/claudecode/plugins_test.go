package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/engine"
)

const testPluginSHA = "0123456789abcdef0123456789abcdef01234567"

func testPluginConfig() PluginConfig {
	return PluginConfig{
		Sources: []PluginSource{
			{Name: "team", RepoURL: "https://gitlab.example.com/team/plugins.git", Ref: testPluginSHA, TokenSecret: "plugin-token"},
			{Name: "public", RepoURL: "https://github.com/example/plugins.git", Ref: "v1.2.0"},
		},
		Default: []PluginRef{{Source: "team", Plugin: "engineer"}},
		Profiles: map[string][]PluginRef{
			"build": {{Source: "team", Plugin: "implementations"}, {Source: "public", Plugin: "browser"}},
			"sre":   {{Source: "team", Plugin: "sre"}, {Source: "team", Plugin: "engineer"}},
		},
	}
}

func TestParsePluginRef(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    PluginRef
		wantErr bool
	}{
		{name: "source and plugin", in: "team/engineer", want: PluginRef{Source: "team", Plugin: "engineer"}},
		{name: "no slash", in: "engineer", wantErr: true},
		{name: "empty source", in: "/engineer", wantErr: true},
		{name: "empty plugin", in: "team/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePluginRef(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPluginConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PluginConfig)
		wantErr string
	}{
		{name: "valid config", mutate: func(*PluginConfig) {}},
		{name: "empty config", mutate: func(c *PluginConfig) { *c = PluginConfig{} }},
		{
			name:    "source name with capitals",
			mutate:  func(c *PluginConfig) { c.Sources[0].Name = "Team" },
			wantErr: "lowercase",
		},
		{
			name:    "duplicate source",
			mutate:  func(c *PluginConfig) { c.Sources[1].Name = "team" },
			wantErr: "more than once",
		},
		{
			name:    "plain http repository",
			mutate:  func(c *PluginConfig) { c.Sources[0].RepoURL = "http://gitlab.example.com/team/plugins.git" },
			wantErr: "https://",
		},
		{
			name:    "https scheme with no host",
			mutate:  func(c *PluginConfig) { c.Sources[0].RepoURL = "https://" },
			wantErr: "with a host",
		},
		{
			name:    "credentials in the URL",
			mutate:  func(c *PluginConfig) { c.Sources[0].RepoURL = "https://bot:secret@gitlab.example.com/team/plugins.git" },
			wantErr: "must not contain credentials",
		},
		{
			name: "token in the query string",
			mutate: func(c *PluginConfig) {
				c.Sources[0].RepoURL = "https://gitlab.example.com/team/plugins.git?access_token=secret"
			},
			wantErr: "must not have a query",
		},
		{
			name:    "fragment",
			mutate:  func(c *PluginConfig) { c.Sources[0].RepoURL = "https://gitlab.example.com/team/plugins.git#main" },
			wantErr: "must not have a query",
		},
		{
			name:    "missing ref",
			mutate:  func(c *PluginConfig) { c.Sources[0].Ref = "" },
			wantErr: "commit SHA or tag",
		},
		{
			name:    "ref that looks like an option",
			mutate:  func(c *PluginConfig) { c.Sources[0].Ref = "--upload-pack=evil" },
			wantErr: "commit SHA or tag",
		},
		{
			name:    "HEAD as the ref",
			mutate:  func(c *PluginConfig) { c.Sources[0].Ref = "HEAD" },
			wantErr: "names a branch",
		},
		{
			name:    "fully qualified branch ref",
			mutate:  func(c *PluginConfig) { c.Sources[0].Ref = "refs/heads/main" },
			wantErr: "names a branch",
		},
		{
			name:    "token key without a secret",
			mutate:  func(c *PluginConfig) { c.Sources[1].TokenKey = "token" },
			wantErr: "need token_secret",
		},
		{
			name:    "default names an unknown source",
			mutate:  func(c *PluginConfig) { c.Default = []PluginRef{{Source: "other", Plugin: "engineer"}} },
			wantErr: `unknown source "other"`,
		},
		{
			name:    "plugin name with shell characters",
			mutate:  func(c *PluginConfig) { c.Default = []PluginRef{{Source: "team", Plugin: "x;rm -rf"}} },
			wantErr: "invalid plugin name",
		},
		{
			name:    "profile names an unknown source",
			mutate:  func(c *PluginConfig) { c.Profiles["build"] = []PluginRef{{Source: "other", Plugin: "x"}} },
			wantErr: "profile build",
		},
		{
			name:    "profile name with a space",
			mutate:  func(c *PluginConfig) { c.Profiles["my build"] = nil },
			wantErr: "plugin profile name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testPluginConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestValidatePluginRepoURLDoesNotLeak checks that no validation error quotes
// a URL, since the controller logs the error at startup.
func TestValidatePluginRepoURLDoesNotLeak(t *testing.T) {
	for _, raw := range []string{
		"https://bot:hunter2@gitlab.example.com/repo%ZZ",
		"https://bot:hunter2@gitlab.example.com/repo.git",
		"https://gitlab.example.com/repo.git?access_token=hunter2",
	} {
		err := validatePluginRepoURL(raw)
		require.Error(t, err, raw)
		assert.NotContains(t, err.Error(), "hunter2", raw)
	}
}

func TestPluginsForTask(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		want    []string
		wantErr string
	}{
		{name: "no labels loads the defaults", want: []string{"team/engineer"}},
		{name: "unrelated labels are ignored", labels: []string{"lang:go", "osmia"}, want: []string{"team/engineer"}},
		{
			name:   "a profile label adds its plugins",
			labels: []string{"osmia-plugins:build"},
			want:   []string{"team/engineer", "team/implementations", "public/browser"},
		},
		{
			name:   "two profiles merge without duplicates",
			labels: []string{"osmia-plugins:sre", "osmia-plugins:build"},
			want:   []string{"team/engineer", "team/sre", "team/implementations", "public/browser"},
		},
		{name: "an unknown profile is an error", labels: []string{"osmia-plugins:biuld"}, wantErr: `unknown plugin profile "biuld"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs, err := testPluginConfig().pluginsForTask(tt.labels)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			got := make([]string, 0, len(refs))
			for _, r := range refs {
				got = append(got, r.String())
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPluginExecutionForTask(t *testing.T) {
	t.Run("only sources the task uses are fetched", func(t *testing.T) {
		exec, err := testPluginConfig().pluginExecutionForTask(nil)
		require.NoError(t, err)

		var specs []pluginSpec
		require.NoError(t, json.Unmarshal([]byte(exec.env[pluginsEnvVar]), &specs))
		assert.Equal(t, []pluginSpec{{
			Source:        "team",
			RepoURL:       "https://gitlab.example.com/team/plugins.git",
			Ref:           testPluginSHA,
			TokenEnv:      "OSMIA_PLUGIN_TOKEN_TEAM",
			TokenUsername: "x-access-token",
			Plugins:       []string{"engineer"},
		}}, specs)
		assert.Equal(t, map[string]engine.SecretKeyRef{
			"OSMIA_PLUGIN_TOKEN_TEAM": {SecretName: "plugin-token", Key: "token"},
		}, exec.secretKeyRefs)
	})

	t.Run("a public source gets no token", func(t *testing.T) {
		exec, err := testPluginConfig().pluginExecutionForTask([]string{"osmia-plugins:build"})
		require.NoError(t, err)

		var specs []pluginSpec
		require.NoError(t, json.Unmarshal([]byte(exec.env[pluginsEnvVar]), &specs))
		require.Len(t, specs, 2)
		assert.Equal(t, "public", specs[1].Source)
		assert.Empty(t, specs[1].TokenEnv)
		assert.Equal(t, []string{"browser"}, specs[1].Plugins)
		assert.Len(t, exec.secretKeyRefs, 1)
	})

	t.Run("custom token key and username", func(t *testing.T) {
		cfg := testPluginConfig()
		cfg.Sources[0].TokenKey = "password"
		cfg.Sources[0].TokenUsername = "gitlab+deploy-token-1"
		exec, err := cfg.pluginExecutionForTask(nil)
		require.NoError(t, err)

		var specs []pluginSpec
		require.NoError(t, json.Unmarshal([]byte(exec.env[pluginsEnvVar]), &specs))
		assert.Equal(t, "gitlab+deploy-token-1", specs[0].TokenUsername)
		assert.Equal(t, "password", exec.secretKeyRefs["OSMIA_PLUGIN_TOKEN_TEAM"].Key)
	})

	t.Run("no plugins sets nothing", func(t *testing.T) {
		exec, err := PluginConfig{}.pluginExecutionForTask([]string{"lang:go"})
		require.NoError(t, err)
		assert.Empty(t, exec.env)
		assert.Empty(t, exec.secretKeyRefs)
	})
}

func TestBuildExecutionSpecPlugins(t *testing.T) {
	task := engine.Task{ID: "t-1", RepoURL: "https://github.com/example/repo", Title: "Fix it"}
	config := engine.EngineConfig{TimeoutSeconds: 3600}

	t.Run("plugins reach the pod", func(t *testing.T) {
		spec, err := New(WithPlugins(testPluginConfig())).BuildExecutionSpec(task, config)
		require.NoError(t, err)
		assert.Contains(t, spec.Env[pluginsEnvVar], `"plugins":["engineer"]`)
		assert.Equal(t, engine.SecretKeyRef{SecretName: "plugin-token", Key: "token"}, spec.SecretKeyRefs["OSMIA_PLUGIN_TOKEN_TEAM"])
	})

	t.Run("an unknown profile fails the build", func(t *testing.T) {
		labelled := task
		labelled.Labels = []string{"osmia-plugins:missing"}
		_, err := New(WithPlugins(testPluginConfig())).BuildExecutionSpec(labelled, config)
		require.ErrorContains(t, err, "choosing plugins")
	})

	t.Run("no plugin config leaves the pod unchanged", func(t *testing.T) {
		spec, err := New().BuildExecutionSpec(task, config)
		require.NoError(t, err)
		assert.NotContains(t, spec.Env, pluginsEnvVar)
	})
}
