package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEngine is a minimal ExecutionEngine implementation for testing the
// registry without pulling in real engine packages.
type stubEngine struct {
	name string
}

func (s *stubEngine) Name() string           { return s.name }
func (s *stubEngine) InterfaceVersion() int   { return 1 }
func (s *stubEngine) BuildPrompt(Task) (string, error) {
	return "stub prompt", nil
}
func (s *stubEngine) BuildExecutionSpec(Task, EngineConfig) (*ExecutionSpec, error) {
	return &ExecutionSpec{Image: "stub"}, nil
}

func TestRegistryRegister(t *testing.T) {
	tests := []struct {
		name    string
		engines []ExecutionEngine
		wantErr string
	}{
		{
			name: "register single engine succeeds",
			engines: []ExecutionEngine{
				&stubEngine{name: "alpha"},
			},
		},
		{
			name: "register multiple engines succeeds",
			engines: []ExecutionEngine{
				&stubEngine{name: "alpha"},
				&stubEngine{name: "beta"},
			},
		},
		{
			name: "duplicate name returns error",
			engines: []ExecutionEngine{
				&stubEngine{name: "alpha"},
				&stubEngine{name: "alpha"},
			},
			wantErr: "already registered",
		},
		{
			name:    "nil engine returns error",
			engines: []ExecutionEngine{nil},
			wantErr: "must not be nil",
		},
		{
			name: "empty name returns error",
			engines: []ExecutionEngine{
				&stubEngine{name: ""},
			},
			wantErr: "must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			var lastErr error
			for _, eng := range tt.engines {
				if err := r.Register(eng); err != nil {
					lastErr = err
				}
			}

			if tt.wantErr != "" {
				require.Error(t, lastErr)
				assert.Contains(t, lastErr.Error(), tt.wantErr)
			} else {
				require.NoError(t, lastErr)
			}
		})
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&stubEngine{name: "alpha"}))
	require.NoError(t, r.Register(&stubEngine{name: "beta"}))

	tests := []struct {
		name    string
		lookup  string
		want    string
		wantErr bool
	}{
		{
			name:   "existing engine is returned",
			lookup: "alpha",
			want:   "alpha",
		},
		{
			name:   "second engine is returned",
			lookup: "beta",
			want:   "beta",
		},
		{
			name:    "missing engine returns error",
			lookup:  "gamma",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := r.Get(tt.lookup)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "not found")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, eng.Name())
		})
	}
}

func TestRegistryList(t *testing.T) {
	tests := []struct {
		name    string
		engines []string
		want    []string
	}{
		{
			name:    "empty registry returns empty list",
			engines: nil,
			want:    []string{},
		},
		{
			name:    "single engine",
			engines: []string{"alpha"},
			want:    []string{"alpha"},
		},
		{
			name:    "multiple engines are sorted",
			engines: []string{"codex", "aider", "claude-code"},
			want:    []string{"aider", "claude-code", "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, name := range tt.engines {
				require.NoError(t, r.Register(&stubEngine{name: name}))
			}

			got := r.List()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryDefaultEngine(t *testing.T) {
	tests := []struct {
		name    string
		engines []string
		want    string
		wantErr bool
	}{
		{
			name:    "empty registry returns error",
			engines: nil,
			wantErr: true,
		},
		{
			name:    "single engine is default",
			engines: []string{"codex"},
			want:    "codex",
		},
		{
			name:    "default is first alphabetically",
			engines: []string{"codex", "aider", "claude-code"},
			want:    "aider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, name := range tt.engines {
				require.NoError(t, r.Register(&stubEngine{name: name}))
			}

			eng, err := r.DefaultEngine()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "no engines registered")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, eng.Name())
		})
	}
}
