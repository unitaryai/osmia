package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/unitaryai/osmia/internal/jobbuilder"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/secretresolver"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	pluginsecrets "github.com/unitaryai/osmia/pkg/plugin/secrets"
)

// stubSecretsBackend is a fake secrets backend returning canned values,
// standing in for Vault or AWS Secrets Manager in resolver tests.
type stubSecretsBackend struct {
	name   string
	values map[string]string
}

func (s *stubSecretsBackend) GetSecret(_ context.Context, key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", fmt.Errorf("no such secret %q", key)
	}
	return v, nil
}

func (s *stubSecretsBackend) GetSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		v, err := s.GetSecret(ctx, k)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func (s *stubSecretsBackend) BuildEnvVars(_ map[string]string) ([]corev1.EnvVar, error) {
	return nil, nil
}

func (s *stubSecretsBackend) Name() string { return s.name }
func (s *stubSecretsBackend) InterfaceVersion() int {
	return pluginsecrets.InterfaceVersion
}

var _ pluginsecrets.Backend = (*stubSecretsBackend)(nil)

// permissivePolicy allows raw refs for the vault and k8s schemes, which is
// what most of these tests exercise. The fail-closed default is covered
// separately by TestResolveTaskSecrets_FailsClosedOnPolicyViolation.
func permissivePolicy() secretresolver.Policy {
	return secretresolver.Policy{
		AllowRawRefs:   true,
		AllowedSchemes: []string{"alias", "k8s", "vault"},
	}
}

func testResolver(t *testing.T, opts ...secretresolver.Option) *secretresolver.Resolver {
	t.Helper()
	base := []secretresolver.Option{
		secretresolver.WithBackend("vault", &stubSecretsBackend{
			name:   "vault",
			values: map[string]string{"secret/data/db#url": "postgres://example"},
		}),
		secretresolver.WithPolicy(permissivePolicy()),
	}
	return secretresolver.NewResolver(append(base, opts...)...)
}

func testReconcilerWithResolver(t *testing.T, r *secretresolver.Resolver) *Reconciler {
	t.Helper()
	k8s := fake.NewSimpleClientset()
	rec := NewReconciler(&config.Config{}, testLogger(),
		WithK8sClient(k8s),
		WithNamespace("test-ns"),
	)
	if r != nil {
		WithSecretsResolver(r)(rec)
	}
	return rec
}

func TestResolveTaskSecrets(t *testing.T) {
	tests := []struct {
		name        string
		task        engine.Task
		wantKeyRefs map[string]engine.SecretKeyRef
		wantValues  map[string]string
	}{
		{
			name: "no secrets declared",
			task: engine.Task{Description: "just do the thing"},
		},
		{
			name: "k8s ref from comment block becomes a key ref",
			task: engine.Task{
				Description: "do the thing\n\n<!-- osmia:secrets\n- ref: k8s://db-secret/url\n  env: DATABASE_URL\n-->\n",
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
			},
		},
		{
			name: "vault ref is resolved to a value",
			task: engine.Task{
				Description: "<!-- osmia:secrets\n- ref: vault://secret/data/db#url\n  env: DATABASE_URL\n-->",
			},
			wantValues: map[string]string{"DATABASE_URL": "postgres://example"},
		},
		{
			name: "label form is honoured alongside the comment block",
			task: engine.Task{
				Description: "<!-- osmia:secrets\n- ref: k8s://db-secret/url\n  env: DATABASE_URL\n-->",
				Labels:      []string{"osmia:secret:CACHE_URL=k8s://cache-secret/url", "unrelated"},
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
				"CACHE_URL":    {SecretName: "cache-secret", Key: "url"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testReconcilerWithResolver(t, testResolver(t))

			plan, err := r.resolveTaskSecrets(context.Background(), tt.task)
			require.NoError(t, err)

			if tt.wantKeyRefs == nil && tt.wantValues == nil {
				assert.Nil(t, plan)
				return
			}

			require.NotNil(t, plan)
			for env, want := range tt.wantKeyRefs {
				assert.Equal(t, want, plan.KeyRefs[env])
			}
			assert.Len(t, plan.KeyRefs, len(tt.wantKeyRefs))
			for env, want := range tt.wantValues {
				assert.Equal(t, want, plan.Values[env])
			}
			assert.Len(t, plan.Values, len(tt.wantValues))
		})
	}
}

// TestResolveTaskSecrets_NoResolverIsNoOp verifies that a controller with no
// secrets resolver configured ignores secret declarations entirely rather
// than failing the launch.
func TestResolveTaskSecrets_NoResolverIsNoOp(t *testing.T) {
	r := testReconcilerWithResolver(t, nil)

	plan, err := r.resolveTaskSecrets(context.Background(), engine.Task{
		Description: "<!-- osmia:secrets\n- ref: k8s://db-secret/url\n  env: DATABASE_URL\n-->",
	})

	require.NoError(t, err)
	assert.Nil(t, plan)
}

// TestResolveTaskSecrets_FailsClosedOnPolicyViolation pins the fail-closed
// contract: with the default policy (raw refs disallowed), a task asking for
// a raw reference is rejected rather than launched without the secret.
func TestResolveTaskSecrets_FailsClosedOnPolicyViolation(t *testing.T) {
	resolver := secretresolver.NewResolver(
		secretresolver.WithPolicy(secretresolver.Policy{}),
	)
	r := testReconcilerWithResolver(t, resolver)

	plan, err := r.resolveTaskSecrets(context.Background(), engine.Task{
		Description: "<!-- osmia:secrets\n- ref: k8s://db-secret/url\n  env: DATABASE_URL\n-->",
	})

	require.Error(t, err)
	assert.Nil(t, plan)
	assert.Contains(t, err.Error(), "raw secret references are not permitted")
}

// TestResolveTaskSecrets_AliasTenantMismatchIsRejected verifies that tenant
// scoping on an alias is enforced through the controller path, not only in
// the resolver's own tests.
func TestResolveTaskSecrets_AliasTenantMismatchIsRejected(t *testing.T) {
	resolver := secretresolver.NewResolver(
		secretresolver.WithPolicy(secretresolver.Policy{
			AllowedSchemes: []string{"alias", "k8s"},
			TenantID:       "tenant-a",
		}),
		secretresolver.WithAliases(map[string]secretresolver.SecretAlias{
			"prod-db": {Name: "prod-db", URI: "k8s://db-secret/url", TenantID: "tenant-b"},
		}),
	)
	r := testReconcilerWithResolver(t, resolver)

	_, err := r.resolveTaskSecrets(context.Background(), engine.Task{
		Description: "<!-- osmia:secrets\n- alias: prod-db\n-->",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
}

func TestApplyTaskSecrets(t *testing.T) {
	tests := []struct {
		name        string
		spec        *engine.ExecutionSpec
		plan        *taskSecrets
		wantErr     string
		wantKeyRefs map[string]engine.SecretKeyRef
	}{
		{
			name: "nil plan leaves the spec untouched",
			spec: &engine.ExecutionSpec{Env: map[string]string{"FOO": "bar"}},
			plan: nil,
		},
		{
			name: "k8s refs are merged as-is",
			spec: &engine.ExecutionSpec{},
			plan: &taskSecrets{
				KeyRefs: map[string]engine.SecretKeyRef{
					"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
				},
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
			},
		},
		{
			name: "resolved values point at the ephemeral secret, never inline",
			spec: &engine.ExecutionSpec{},
			plan: &taskSecrets{
				Values: map[string]string{"DATABASE_URL": "postgres://example"},
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "osmia-task-secrets-tr-1", Key: "DATABASE_URL"},
			},
		},
		{
			name: "collision with an engine env var is rejected",
			spec: &engine.ExecutionSpec{
				Env: map[string]string{"ANTHROPIC_API_KEY": "sk-real"},
			},
			plan: &taskSecrets{
				Values: map[string]string{"ANTHROPIC_API_KEY": "sk-attacker"},
			},
			wantErr: "collides with an environment variable set by the engine",
		},
		{
			name: "collision with an engine secret key ref is rejected",
			spec: &engine.ExecutionSpec{
				SecretKeyRefs: map[string]engine.SecretKeyRef{
					"ANTHROPIC_API_KEY": {SecretName: "anthropic", Key: "key"},
				},
			},
			plan: &taskSecrets{
				KeyRefs: map[string]engine.SecretKeyRef{
					"ANTHROPIC_API_KEY": {SecretName: "attacker", Key: "key"},
				},
			},
			wantErr: "collides with a secret reference set by the engine",
		},
		{
			name: "collision with an engine secret mount is rejected",
			spec: &engine.ExecutionSpec{
				SecretEnv: map[string]string{"GITLAB_TOKEN": "scm-secret"},
			},
			plan: &taskSecrets{
				Values: map[string]string{"GITLAB_TOKEN": "attacker"},
			},
			wantErr: "collides with a secret mount set by the engine",
		},
		{
			name: "with Bedrock, an Anthropic API key is refused",
			spec: bedrockSpec(t),
			plan: &taskSecrets{
				Values: map[string]string{"ANTHROPIC_API_KEY": "sk-attacker"},
			},
			wantErr: "is reserved for the model provider",
		},
		{
			name: "with Bedrock, an Anthropic endpoint is refused",
			spec: bedrockSpec(t),
			plan: &taskSecrets{
				Values: map[string]string{"ANTHROPIC_BEDROCK_BASE_URL": "https://attacker.example.com"},
			},
			wantErr: "is reserved for the model provider",
		},
		{
			name: "with Bedrock, an AWS credential is refused",
			spec: bedrockSpec(t),
			plan: &taskSecrets{
				KeyRefs: map[string]engine.SecretKeyRef{
					"AWS_ACCESS_KEY_ID": {SecretName: "attacker", Key: "id"},
				},
			},
			wantErr: "is reserved for the model provider",
		},
		{
			name: "with Bedrock, a Claude Code setting is refused",
			spec: bedrockSpec(t),
			plan: &taskSecrets{
				Values: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "0"},
			},
			wantErr: "is reserved for the model provider",
		},
		{
			name: "with Bedrock, another name is accepted",
			spec: bedrockSpec(t),
			plan: &taskSecrets{
				KeyRefs: map[string]engine.SecretKeyRef{
					"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
				},
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
			},
		},
		{
			name: "without Bedrock, an AWS name the engine does not set is accepted",
			spec: &engine.ExecutionSpec{},
			plan: &taskSecrets{
				KeyRefs: map[string]engine.SecretKeyRef{
					"AWS_ACCESS_KEY_ID": {SecretName: "deploy", Key: "id"},
				},
			},
			wantKeyRefs: map[string]engine.SecretKeyRef{
				"AWS_ACCESS_KEY_ID": {SecretName: "deploy", Key: "id"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applyTaskSecrets(tt.spec, tt.plan, taskSecretName("tr-1"))

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			for env, want := range tt.wantKeyRefs {
				assert.Equal(t, want, tt.spec.SecretKeyRefs[env])
			}
			assert.Len(t, tt.spec.SecretKeyRefs, len(tt.wantKeyRefs))
		})
	}
}

// bedrockSpec returns the execution spec the Claude Code engine builds when
// it calls Amazon Bedrock.
func bedrockSpec(t *testing.T) *engine.ExecutionSpec {
	t.Helper()
	e := claudecode.New(claudecode.WithBedrock("example-region-1", claudecode.BedrockModels{
		Main:   "example-main-model",
		Opus:   "example-opus-model",
		Sonnet: "example-sonnet-model",
		Haiku:  "example-haiku-model",
	}))
	spec, err := e.BuildExecutionSpec(engine.Task{ID: "task-1", Title: "Fix it"}, engine.EngineConfig{})
	require.NoError(t, err)
	return spec
}

// TestApplyTaskSecrets_ValuesNeverReachSpecEnv is the security invariant the
// ephemeral-secret indirection exists for: a resolved plaintext value must
// never be written into the execution spec's plain Env map, where it would
// appear in the Job manifest.
func TestApplyTaskSecrets_ValuesNeverReachSpecEnv(t *testing.T) {
	spec := &engine.ExecutionSpec{Env: map[string]string{"EXISTING": "value"}}
	plan := &taskSecrets{Values: map[string]string{"DATABASE_URL": "postgres://sensitive"}}

	require.NoError(t, applyTaskSecrets(spec, plan, taskSecretName("tr-1")))

	for _, v := range spec.Env {
		assert.NotContains(t, v, "sensitive")
	}
	assert.NotContains(t, spec.Env, "DATABASE_URL")
}

func TestCreateTaskSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("writes resolved values to an opaque secret", func(t *testing.T) {
		r := testReconcilerWithResolver(t, nil)
		plan := &taskSecrets{Values: map[string]string{"DATABASE_URL": "postgres://example"}}

		require.NoError(t, r.createTaskSecret(ctx, "tr-1", "claude-code", plan))

		got, err := r.k8sClient.CoreV1().Secrets("test-ns").
			Get(ctx, "osmia-task-secrets-tr-1", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, corev1.SecretTypeOpaque, got.Type)
		assert.Equal(t, "postgres://example", got.StringData["DATABASE_URL"])
		assert.Equal(t, "tr-1", got.Labels["osmia.io/task-run-id"])
	})

	t.Run("no secret is created when the plan holds only k8s refs", func(t *testing.T) {
		r := testReconcilerWithResolver(t, nil)
		plan := &taskSecrets{
			KeyRefs: map[string]engine.SecretKeyRef{
				"DATABASE_URL": {SecretName: "db-secret", Key: "url"},
			},
		}

		require.NoError(t, r.createTaskSecret(ctx, "tr-1", "claude-code", plan))

		_, err := r.k8sClient.CoreV1().Secrets("test-ns").
			Get(ctx, "osmia-task-secrets-tr-1", metav1.GetOptions{})
		assert.Error(t, err)
	})
}

// TestDeleteTaskSecret verifies the abort path removes the ephemeral secret
// when no Job was ever created to own it.
func TestDeleteTaskSecret(t *testing.T) {
	ctx := context.Background()
	r := testReconcilerWithResolver(t, nil)
	plan := &taskSecrets{Values: map[string]string{"DATABASE_URL": "postgres://example"}}

	require.NoError(t, r.createTaskSecret(ctx, "tr-1", "claude-code", plan))
	require.NoError(t, r.deleteTaskSecret(ctx, "tr-1", plan))

	_, err := r.k8sClient.CoreV1().Secrets("test-ns").
		Get(ctx, "osmia-task-secrets-tr-1", metav1.GetOptions{})
	assert.Error(t, err)
}

// TestSweepOrphanedTaskSecrets covers the gap the abort paths cannot: a
// controller killed between creating an ephemeral Secret and the Job
// adopting it leaves plaintext credentials with no owner and nothing to
// collect them.
func TestSweepOrphanedTaskSecrets(t *testing.T) {
	ctx := context.Background()

	taskSecret := func(name string, age time.Duration, owners []metav1.OwnerReference) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "test-ns",
				Labels: map[string]string{
					labelComponent:            componentAgent,
					labelManagedBy:            managedByOsmia,
					labelSecretPurpose:        secretPurposeTaskSecret,
					jobbuilder.LabelTaskRunID: "tr-1",
				},
				CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
				OwnerReferences:   owners,
			},
		}
	}

	ownedByJob := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "osmia-tr-1"}}

	tests := []struct {
		name     string
		objects  []runtime.Object
		wantGone []string
		wantKept []string
	}{
		{
			name:     "an unadopted secret past the grace period is deleted",
			objects:  []runtime.Object{taskSecret("orphan", taskSecretOrphanGrace+time.Minute, nil)},
			wantGone: []string{"orphan"},
		},
		{
			name:     "an unadopted secret inside the grace period is left alone",
			objects:  []runtime.Object{taskSecret("just-created", time.Second, nil)},
			wantKept: []string{"just-created"},
		},
		{
			name:     "an adopted secret is left to Kubernetes garbage collection",
			objects:  []runtime.Object{taskSecret("adopted", taskSecretOrphanGrace+time.Hour, ownedByJob)},
			wantKept: []string{"adopted"},
		},
		{
			name: "secrets that are not task secrets are never touched",
			objects: []runtime.Object{
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
					Name:              "anthropic-secret",
					Namespace:         "test-ns",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour)),
				}},
			},
			wantKept: []string{"anthropic-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := fake.NewSimpleClientset(tt.objects...)
			r := NewReconciler(&config.Config{}, testLogger(),
				WithK8sClient(k8s),
				WithNamespace("test-ns"),
			)

			r.sweepOrphanedTaskSecrets(ctx)

			for _, name := range tt.wantGone {
				_, err := k8s.CoreV1().Secrets("test-ns").Get(ctx, name, metav1.GetOptions{})
				assert.Truef(t, apierrors.IsNotFound(err), "expected %q to be deleted, got %v", name, err)
			}
			for _, name := range tt.wantKept {
				_, err := k8s.CoreV1().Secrets("test-ns").Get(ctx, name, metav1.GetOptions{})
				assert.NoErrorf(t, err, "expected %q to survive the sweep", name)
			}
		})
	}
}

// TestSweepOrphanedTaskSecretsWithoutClient guards the nil-client path used
// by tests and by the local backend, where there is nothing to sweep.
func TestSweepOrphanedTaskSecretsWithoutClient(t *testing.T) {
	r := NewReconciler(&config.Config{}, testLogger(), WithNamespace("test-ns"))
	assert.NotPanics(t, func() { r.sweepOrphanedTaskSecrets(context.Background()) })
}
