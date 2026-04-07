// Package plugin implements the gRPC plugin host for loading and managing
// external Osmia plugins. Built-in plugins are compiled into the controller
// binary. External plugins run as separate processes communicating over gRPC
// via hashicorp/go-plugin.
package plugin

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
)

// PluginType identifies the category of a plugin.
type PluginType string

const (
	// PluginTypeTicketing is a ticketing backend plugin.
	PluginTypeTicketing PluginType = "ticketing"
	// PluginTypeNotifications is a notification channel plugin.
	PluginTypeNotifications PluginType = "notifications"
	// PluginTypeApproval is a human approval backend plugin.
	PluginTypeApproval PluginType = "approval"
	// PluginTypeSecrets is a secrets backend plugin.
	PluginTypeSecrets PluginType = "secrets"
	// PluginTypeReview is a review backend plugin.
	PluginTypeReview PluginType = "review"
	// PluginTypeSCM is an SCM backend plugin.
	PluginTypeSCM PluginType = "scm"
)

// PluginConfig describes an external plugin to be loaded by the host.
type PluginConfig struct {
	Name             string     `yaml:"name"`
	Command          string     `yaml:"command"`
	Args             []string   `yaml:"args,omitempty"`
	Type             PluginType `yaml:"type"`
	InterfaceVersion int        `yaml:"interface_version"`
}

// HealthConfig controls plugin health monitoring and restart behaviour.
type HealthConfig struct {
	MaxRestarts    int   `yaml:"max_plugin_restarts"`
	RestartBackoff []int `yaml:"restart_backoff"` // seconds between restarts
}

// DefaultHealthConfig returns sensible defaults for plugin health monitoring.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		MaxRestarts:    3,
		RestartBackoff: []int{1, 5, 30},
	}
}

// pluginInstance tracks the lifecycle of a single external plugin.
type pluginInstance struct {
	config       PluginConfig
	client       *goplugin.Client
	restartCount int
	healthy      bool
	lastError    error
	startedAt    time.Time
}

// Host manages the lifecycle of all external plugins. It handles spawning,
// health checking, and restarting plugin subprocesses.
type Host struct {
	mu           sync.RWMutex
	plugins      map[string]*pluginInstance
	healthConfig HealthConfig
	logger       *slog.Logger
	handshake    goplugin.HandshakeConfig
}

// NewHost creates a new plugin host with the given health configuration.
func NewHost(healthCfg HealthConfig, logger *slog.Logger) *Host {
	return &Host{
		plugins:      make(map[string]*pluginInstance),
		healthConfig: healthCfg,
		logger:       logger,
		handshake: goplugin.HandshakeConfig{
			ProtocolVersion:  1,
			MagicCookieKey:   "OSMIA_PLUGIN",
			MagicCookieValue: "osmia",
		},
	}
}

// knownInterfaceVersions maps plugin types to the current interface version
// expected by the controller. External plugins declaring a different version
// are refused at load time.
var knownInterfaceVersions = map[PluginType]int{
	PluginTypeTicketing:     1,
	PluginTypeNotifications: 3,
	PluginTypeApproval:      1,
	PluginTypeSecrets:       1,
	PluginTypeReview:        1,
	PluginTypeSCM:           2,
}

// LoadPlugin validates and starts an external plugin subprocess. Validation is
// two-level:
//
//  1. Pre-spawn config check — the `InterfaceVersion` declared in cfg is
//     compared against the controller's expected version for that plugin type
//     (knownInterfaceVersions). Mismatches are rejected immediately without
//     spawning a subprocess. This guards against obviously misconfigured plugins
//     but relies on the operator setting InterfaceVersion correctly in config.
//
//  2. Transport handshake — hashicorp/go-plugin verifies the magic cookie
//     ("OSMIA_PLUGIN"/"osmia") when the subprocess connects. This confirms
//     the binary is a valid Osmia plugin but does not verify which interface
//     version it implements.
//
// Note: the controller does not currently call the protobuf Handshake RPC to
// verify the version the binary actually implements. That requires generated
// proto stubs (run `make sdk-gen`). Until then, operators must ensure the
// interface_version in config matches the binary.
func (h *Host) LoadPlugin(cfg PluginConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.plugins[cfg.Name]; exists {
		return fmt.Errorf("plugin %q already loaded", cfg.Name)
	}

	if err := validateInterfaceVersion(h.logger, cfg); err != nil {
		return err
	}

	instance, err := h.startPlugin(cfg)
	if err != nil {
		return fmt.Errorf("starting plugin %q: %w", cfg.Name, err)
	}

	h.plugins[cfg.Name] = instance
	h.logger.Info("plugin loaded",
		"plugin", cfg.Name,
		"type", cfg.Type,
		"interface_version", cfg.InterfaceVersion,
	)

	return nil
}

// validateInterfaceVersion enforces the controller's expected interface version
// for a plugin before any subprocess is started.
func validateInterfaceVersion(logger *slog.Logger, cfg PluginConfig) error {
	expected, ok := knownInterfaceVersions[cfg.Type]
	if ok && cfg.InterfaceVersion != expected {
		logger.Error("plugin interface version mismatch",
			"plugin", cfg.Name,
			"type", cfg.Type,
			"expected_version", expected,
			"declared_version", cfg.InterfaceVersion,
		)
		return fmt.Errorf("plugin %q declares interface version %d but controller expects %d for %s plugins; update the plugin or controller",
			cfg.Name, cfg.InterfaceVersion, expected, cfg.Type)
	}

	return nil
}

// startPlugin creates and starts a new plugin subprocess. It performs the
// hashicorp/go-plugin transport handshake (magic cookie) but does not call
// the protobuf Handshake RPC — that requires generated proto stubs.
func (h *Host) startPlugin(cfg PluginConfig) (*pluginInstance, error) {
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: h.handshake,
		Plugins:         map[string]goplugin.Plugin{},
		Cmd:             exec.Command(cfg.Command, cfg.Args...),
		AllowedProtocols: []goplugin.Protocol{
			goplugin.ProtocolGRPC,
		},
		Logger: nil, // Uses slog via adapter
	})

	// Establish the gRPC connection and perform the transport-level handshake
	// (magic cookie verification). The protobuf Handshake RPC is not called
	// here; version negotiation is enforced by the pre-spawn config check in
	// LoadPlugin combined with the operator's InterfaceVersion setting.
	_, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("connecting to plugin: %w", err)
	}

	return &pluginInstance{
		config:    cfg,
		client:    client,
		healthy:   true,
		startedAt: time.Now(),
	}, nil
}

// RestartPlugin attempts to restart a failed plugin with exponential backoff.
// It returns an error if the maximum number of restarts has been exceeded.
func (h *Host) RestartPlugin(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	instance, exists := h.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %q not found", name)
	}

	if instance.restartCount >= h.healthConfig.MaxRestarts {
		instance.healthy = false
		return fmt.Errorf("plugin %q exceeded maximum restarts (%d)", name, h.healthConfig.MaxRestarts)
	}

	// Calculate backoff duration.
	backoffIdx := instance.restartCount
	if backoffIdx >= len(h.healthConfig.RestartBackoff) {
		backoffIdx = len(h.healthConfig.RestartBackoff) - 1
	}
	backoff := time.Duration(h.healthConfig.RestartBackoff[backoffIdx]) * time.Second

	h.logger.Info("restarting plugin",
		"plugin", name,
		"restart_count", instance.restartCount+1,
		"backoff_seconds", h.healthConfig.RestartBackoff[backoffIdx],
	)

	// Kill the old client if it is still running.
	if instance.client != nil {
		instance.client.Kill()
	}

	time.Sleep(backoff)

	newInstance, err := h.startPlugin(instance.config)
	if err != nil {
		instance.restartCount++
		instance.healthy = false
		instance.lastError = err
		return fmt.Errorf("restarting plugin %q (attempt %d): %w", name, instance.restartCount, err)
	}

	newInstance.restartCount = instance.restartCount + 1
	h.plugins[name] = newInstance

	return nil
}

// GetPlugin returns the client for a loaded plugin. Returns an error if
// the plugin is not loaded or is unhealthy.
func (h *Host) GetPlugin(name string) (*goplugin.Client, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	instance, exists := h.plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin %q not loaded", name)
	}

	if !instance.healthy {
		return nil, fmt.Errorf("plugin %q is unhealthy: %v", name, instance.lastError)
	}

	return instance.client, nil
}

// IsHealthy returns whether the named plugin is currently healthy.
func (h *Host) IsHealthy(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	instance, exists := h.plugins[name]
	if !exists {
		return false
	}

	return instance.healthy
}

// ListPlugins returns the names and health status of all loaded plugins.
func (h *Host) ListPlugins() map[string]bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string]bool, len(h.plugins))
	for name, instance := range h.plugins {
		result[name] = instance.healthy
	}
	return result
}

// Shutdown gracefully terminates all loaded plugin subprocesses.
func (h *Host) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for name, instance := range h.plugins {
		if instance.client != nil {
			h.logger.Info("shutting down plugin", "plugin", name)
			instance.client.Kill()
		}
	}
	h.plugins = make(map[string]*pluginInstance)
}
