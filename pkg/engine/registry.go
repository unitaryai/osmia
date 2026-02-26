package engine

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds a collection of named ExecutionEngine implementations
// and supports look-up by engine name. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	engines map[string]ExecutionEngine
}

// NewRegistry returns an empty engine registry.
func NewRegistry() *Registry {
	return &Registry{
		engines: make(map[string]ExecutionEngine),
	}
}

// Register adds an engine to the registry. It returns an error if an
// engine with the same name is already registered.
func (r *Registry) Register(eng ExecutionEngine) error {
	if eng == nil {
		return fmt.Errorf("engine must not be nil")
	}

	name := eng.Name()
	if name == "" {
		return fmt.Errorf("engine name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.engines[name]; exists {
		return fmt.Errorf("engine %q is already registered", name)
	}

	r.engines[name] = eng
	return nil
}

// Get retrieves an engine by name. It returns an error if the engine is
// not found.
func (r *Registry) Get(name string) (ExecutionEngine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	eng, ok := r.engines[name]
	if !ok {
		return nil, fmt.Errorf("engine %q not found", name)
	}

	return eng, nil
}

// List returns a sorted list of all registered engine names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.engines))
	for name := range r.engines {
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

// DefaultEngine returns the first registered engine in alphabetical order.
// It returns an error if the registry is empty.
func (r *Registry) DefaultEngine() (ExecutionEngine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.engines) == 0 {
		return nil, fmt.Errorf("no engines registered")
	}

	names := make([]string, 0, len(r.engines))
	for name := range r.engines {
		names = append(names, name)
	}

	sort.Strings(names)
	return r.engines[names[0]], nil
}
