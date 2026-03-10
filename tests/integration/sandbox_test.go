//go:build integration

// Package integration_test contains integration tests that verify the
// SandboxBuilder produces correctly configured Kubernetes Jobs with gVisor
// runtime isolation, warm pool labels, and environment stripping annotations.
package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/sandboxbuilder"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
)

// sandboxTestSpec returns a standard ExecutionSpec for sandbox builder tests.
func sandboxTestSpec() *engine.ExecutionSpec {
	eng := claudecode.New()
	spec, _ := eng.BuildExecutionSpec(engine.Task{
		ID:       "sandbox-test-1",
		TicketID: "TICKET-SB-1",
		Title:    "Sandbox test task",
		RepoURL:  "https://github.com/org/repo",
	}, engine.EngineConfig{})
	return spec
}

// TestSandboxBuilderDefaultConfig verifies that a SandboxBuilder with default
// configuration produces a Job with gVisor RuntimeClassName and the
// SandboxClaim annotation.
func TestSandboxBuilderDefaultConfig(t *testing.T) {
	t.Parallel()

	sb := sandboxbuilder.New("test-ns", config.SandboxConfig{})
	spec := sandboxTestSpec()

	job, err := sb.Build("tr-sb-1", "claude-code", spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	// Verify RuntimeClassName defaults to gvisor.
	require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "gvisor", *job.Spec.Template.Spec.RuntimeClassName,
		"default RuntimeClassName must be gvisor")

	// Verify SandboxClaim annotation is present.
	assert.Equal(t, "true", job.Spec.Template.ObjectMeta.Annotations["sandbox.kubernetes.io/claim"],
		"sandbox claim annotation must be present")
}

// TestSandboxBuilderKataRuntime verifies that specifying RuntimeClass:"kata"
// produces a Job with the kata RuntimeClassName.
func TestSandboxBuilderKataRuntime(t *testing.T) {
	t.Parallel()

	sb := sandboxbuilder.New("test-ns", config.SandboxConfig{
		RuntimeClass: "kata",
	})
	spec := sandboxTestSpec()

	job, err := sb.Build("tr-sb-2", "claude-code", spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	require.NotNil(t, job.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "kata", *job.Spec.Template.Spec.RuntimeClassName,
		"RuntimeClassName must respect kata override")
}

// TestSandboxBuilderWarmPoolLabels verifies that enabling the warm pool adds
// the expected label with the engine name as value.
func TestSandboxBuilderWarmPoolLabels(t *testing.T) {
	t.Parallel()

	sb := sandboxbuilder.New("test-ns", config.SandboxConfig{
		WarmPool: config.WarmPoolConfig{Enabled: true, Size: 3},
	})
	spec := sandboxTestSpec()

	job, err := sb.Build("tr-sb-3", "claude-code", spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	podLabels := job.Spec.Template.ObjectMeta.Labels
	assert.Equal(t, "claude-code", podLabels["sandbox.kubernetes.io/warm-pool"],
		"warm pool label must contain the engine name")
}

// TestSandboxBuilderEnvStrippingAnnotation verifies that enabling EnvStripping
// adds the corresponding annotation to the pod template.
func TestSandboxBuilderEnvStrippingAnnotation(t *testing.T) {
	t.Parallel()

	sb := sandboxbuilder.New("test-ns", config.SandboxConfig{
		EnvStripping: true,
	})
	spec := sandboxTestSpec()

	job, err := sb.Build("tr-sb-4", "claude-code", spec)
	require.NoError(t, err)
	require.NotNil(t, job)

	assert.Equal(t, "true", job.Spec.Template.ObjectMeta.Annotations["osmia.io/env-stripping"],
		"env stripping annotation must be present when enabled")
}

// TestSandboxBuilderSecurityContextMatchesStandard verifies that the sandbox
// builder produces the same security context constraints as the standard
// JobBuilder: RunAsNonRoot, ReadOnlyRootFilesystem, no privilege escalation,
// and all capabilities dropped.
func TestSandboxBuilderSecurityContextMatchesStandard(t *testing.T) {
	t.Parallel()

	spec := sandboxTestSpec()

	// Build with standard JobBuilder.
	jb := jobbuilder.NewJobBuilder("test-ns")
	stdJob, err := jb.Build("tr-std-1", "claude-code", spec)
	require.NoError(t, err)

	// Build with SandboxBuilder.
	sb := sandboxbuilder.New("test-ns", config.SandboxConfig{})
	sbJob, err := sb.Build("tr-sb-5", "claude-code", spec)
	require.NoError(t, err)

	require.Len(t, stdJob.Spec.Template.Spec.Containers, 1)
	require.Len(t, sbJob.Spec.Template.Spec.Containers, 1)

	stdSC := stdJob.Spec.Template.Spec.Containers[0].SecurityContext
	sbSC := sbJob.Spec.Template.Spec.Containers[0].SecurityContext

	require.NotNil(t, stdSC)
	require.NotNil(t, sbSC)

	assert.Equal(t, *stdSC.RunAsNonRoot, *sbSC.RunAsNonRoot,
		"RunAsNonRoot must match between standard and sandbox builders")
	assert.Equal(t, *stdSC.ReadOnlyRootFilesystem, *sbSC.ReadOnlyRootFilesystem,
		"ReadOnlyRootFilesystem must match between standard and sandbox builders")
	assert.Equal(t, *stdSC.AllowPrivilegeEscalation, *sbSC.AllowPrivilegeEscalation,
		"AllowPrivilegeEscalation must match between standard and sandbox builders")

	require.NotNil(t, stdSC.Capabilities)
	require.NotNil(t, sbSC.Capabilities)
	assert.Equal(t, stdSC.Capabilities.Drop, sbSC.Capabilities.Drop,
		"dropped capabilities must match between standard and sandbox builders")
}
