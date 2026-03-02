// Package main is the entrypoint for the RoboDev controller binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/unitaryai/robodev/internal/config"
	"github.com/unitaryai/robodev/internal/controller"
	"github.com/unitaryai/robodev/internal/jobbuilder"
	"github.com/unitaryai/robodev/internal/memory"
	"github.com/unitaryai/robodev/internal/prm"
	"github.com/unitaryai/robodev/internal/sandboxbuilder"
	"github.com/unitaryai/robodev/internal/webhook"
	"github.com/unitaryai/robodev/pkg/engine/claudecode"
	"github.com/unitaryai/robodev/pkg/engine/cline"
	"github.com/unitaryai/robodev/pkg/engine/opencode"
	"github.com/unitaryai/robodev/pkg/plugin/ticketing"
	ghticket "github.com/unitaryai/robodev/pkg/plugin/ticketing/github"
	noopticket "github.com/unitaryai/robodev/pkg/plugin/ticketing/noop"
	scticket "github.com/unitaryai/robodev/pkg/plugin/ticketing/shortcut"

	// Notification backends — imported conditionally.
	slacknotify "github.com/unitaryai/robodev/pkg/plugin/notifications/slack"

	// Register metrics with the default Prometheus registry.
	_ "github.com/unitaryai/robodev/internal/metrics"
)

func main() {
	var (
		configPath   = flag.String("config", "/etc/robodev/config.yaml", "path to the RoboDev configuration file")
		metricsAddr  = flag.String("metrics-addr", ":8080", "address for the Prometheus metrics and health endpoints")
		pollInterval = flag.Duration("poll-interval", 30*time.Second, "interval between ticketing backend polls")
		namespace    = flag.String("namespace", "robodev", "kubernetes namespace for job creation")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("starting robodev controller",
		"config", *configPath,
		"metrics_addr", *metricsAddr,
		"poll_interval", *pollInterval,
		"namespace", *namespace,
	)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger.Info("configuration loaded",
		"ticketing_backend", cfg.Ticketing.Backend,
		"default_engine", cfg.Engines.Default,
	)

	// --- Build Kubernetes client ---
	k8sClient, err := buildK8sClient()
	if err != nil {
		logger.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}
	logger.Info("kubernetes client initialised")

	// Collect reconciler options.
	opts := []controller.ReconcilerOption{
		controller.WithNamespace(*namespace),
		controller.WithK8sClient(k8sClient),
	}

	// --- Ticketing backend ---
	var scBackend *scticket.ShortcutBackend
	if cfg.Ticketing.Backend == "github" {
		ghBackend, ghErr := initGitHubBackend(cfg, k8sClient, *namespace, logger)
		if ghErr != nil {
			logger.Error("failed to initialise github ticketing backend", "error", ghErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithTicketing(ghBackend))
		logger.Info("github ticketing backend initialised")
	} else if cfg.Ticketing.Backend == "shortcut" {
		var scErr error
		scBackend, scErr = initShortcutBackend(cfg, k8sClient, *namespace, logger)
		if scErr != nil {
			logger.Error("failed to initialise shortcut ticketing backend", "error", scErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithTicketing(scBackend))
		logger.Info("shortcut ticketing backend initialised",
			"workflow_state_id", scBackend.WorkflowStateID(),
			"in_progress_state_id", scBackend.InProgressStateID(),
		)
	} else if cfg.Ticketing.Backend != "" {
		logger.Error("unsupported ticketing backend", "backend", cfg.Ticketing.Backend)
		os.Exit(1)
	} else {
		// Check for a task_file in the ticketing config (file-watcher mode).
		if taskFile, _, err := configStringOptional(cfg.Ticketing.Config, "task_file"); err != nil {
			logger.Error("invalid task_file config", "error", err)
			os.Exit(1)
		} else if taskFile != "" {
			opts = append(opts, controller.WithTicketing(noopticket.NewWithTaskFile(logger, taskFile)))
			logger.Info("noop ticketing with file-watcher enabled", "task_file", taskFile)
		} else {
			opts = append(opts, controller.WithTicketing(noopticket.New()))
			logger.Info("no ticketing backend configured, using noop fallback")
		}
	}

	// --- Execution engines ---
	claudeEngine := claudecode.New()
	opts = append(opts, controller.WithEngine(claudeEngine))
	logger.Info("claude-code engine registered")

	if cfg.Engines.OpenCode != nil {
		var ocOpts []opencode.Option
		switch cfg.Engines.OpenCode.Provider {
		case "openai":
			ocOpts = append(ocOpts, opencode.WithProvider(opencode.ProviderOpenAI))
		case "google":
			ocOpts = append(ocOpts, opencode.WithProvider(opencode.ProviderGoogle))
		}
		ocEngine := opencode.New(ocOpts...)
		opts = append(opts, controller.WithEngine(ocEngine))
		logger.Info("opencode engine registered")
	}

	if cfg.Engines.Cline != nil {
		var clOpts []cline.Option
		switch cfg.Engines.Cline.Provider {
		case "openai":
			clOpts = append(clOpts, cline.WithProvider(cline.ProviderOpenAI))
		case "google":
			clOpts = append(clOpts, cline.WithProvider(cline.ProviderGoogle))
		case "bedrock":
			clOpts = append(clOpts, cline.WithProvider(cline.ProviderBedrock))
		}
		if cfg.Engines.Cline.MCPEnabled {
			clOpts = append(clOpts, cline.WithMCPEnabled(true))
		}
		clEngine := cline.New(clOpts...)
		opts = append(opts, controller.WithEngine(clEngine))
		logger.Info("cline engine registered")
	}

	// --- Job builder ---
	var jb controller.JobBuilder
	switch cfg.Execution.Backend {
	case "local":
		jb = jobbuilder.NewDockerBuilder(*namespace)
		logger.Info("using local docker job builder")
	case "sandbox":
		jb = sandboxbuilder.New(*namespace, cfg.Execution.Sandbox)
		logger.Info("using sandbox job builder",
			"runtime_class", cfg.Execution.Sandbox.RuntimeClass,
		)
	default:
		jb = jobbuilder.NewJobBuilder(*namespace)
		logger.Info("using standard kubernetes job builder")
	}
	opts = append(opts, controller.WithJobBuilder(jb))

	// --- Notification channels (non-critical) ---
	for _, chCfg := range cfg.Notifications.Channels {
		if chCfg.Backend == "slack" {
			slackCh, slackErr := initSlackChannel(chCfg, k8sClient, *namespace, logger)
			if slackErr != nil {
				logger.Warn("failed to initialise slack notifications, continuing without",
					"error", slackErr,
				)
				continue
			}
			opts = append(opts, controller.WithNotifier(slackCh))
			logger.Info("slack notification channel initialised")
		}
	}

	// Set up context and signal handling early so background goroutines
	// (such as the memory decay loop) can observe cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// --- PRM (Process Reward Model) ---
	if cfg.PRM.Enabled {
		prmCfg := prm.Config{
			Enabled:                cfg.PRM.Enabled,
			EvaluationInterval:     cfg.PRM.EvaluationInterval,
			WindowSize:             cfg.PRM.WindowSize,
			ScoreThresholdNudge:    cfg.PRM.ScoreThresholdNudge,
			ScoreThresholdEscalate: cfg.PRM.ScoreThresholdEscalate,
			HintFilePath:           cfg.PRM.HintFilePath,
			MaxTrajectoryLength:    cfg.PRM.MaxTrajectoryLength,
		}
		opts = append(opts, controller.WithPRMConfig(prmCfg))
		logger.Info("prm evaluator enabled",
			"evaluation_interval", prmCfg.EvaluationInterval,
			"nudge_threshold", prmCfg.ScoreThresholdNudge,
			"escalate_threshold", prmCfg.ScoreThresholdEscalate,
		)
	}

	// --- Memory (cross-task episodic knowledge) ---
	var memoryStore *memory.SQLiteStore
	if cfg.Memory.Enabled {
		storePath := cfg.Memory.StorePath
		if storePath == "" {
			storePath = "/var/lib/robodev/memory.db"
		}

		var storeErr error
		memoryStore, storeErr = memory.NewSQLiteStore(storePath, logger.With("component", "memory-store"))
		if storeErr != nil {
			logger.Error("failed to open memory store", "path", storePath, "error", storeErr)
			os.Exit(1)
		}

		graph := memory.NewGraph(memoryStore, logger.With("component", "memory-graph"))
		if err := graph.LoadFromStore(context.Background()); err != nil {
			logger.Error("failed to load memory graph from store", "error", err)
			os.Exit(1)
		}

		extractor := memory.NewExtractor(logger.With("component", "memory-extractor"))
		queryEngine := memory.NewQueryEngine(graph, logger.With("component", "memory-query"))
		opts = append(opts, controller.WithMemory(graph, extractor, queryEngine))

		logger.Info("memory subsystem enabled",
			"store_path", storePath,
			"node_count", graph.NodeCount(),
			"decay_interval_hours", cfg.Memory.DecayIntervalHours,
		)

		// Start background decay goroutine.
		decayInterval := time.Duration(cfg.Memory.DecayIntervalHours) * time.Hour
		if decayInterval <= 0 {
			decayInterval = 24 * time.Hour
		}
		pruneThreshold := cfg.Memory.PruneThreshold
		if pruneThreshold <= 0 {
			pruneThreshold = 0.05
		}
		go func() {
			ticker := time.NewTicker(decayInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					graph.DecayConfidence(context.Background())
					pruned := graph.PruneStale(context.Background(), pruneThreshold)
					if pruned > 0 {
						logger.Info("memory decay cycle completed", "pruned_nodes", pruned)
					}
				}
			}
		}()
	}

	// Readiness flag — set to true once the controller is fully initialised.
	var ready atomic.Bool

	// Set up HTTP server for metrics and health probes.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
		}
	})

	srv := &http.Server{
		Addr:              *metricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Start the HTTP server in a goroutine.
	go func() {
		logger.Info("starting metrics and health server", "addr", *metricsAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Create the reconciler with all backends wired up.
	reconciler := controller.NewReconciler(cfg, logger, opts...)

	// --- Webhook server (optional) ---
	var webhookSrv *webhook.Server
	if cfg.Webhook.Enabled {
		webhookPort := cfg.Webhook.Port
		if webhookPort == 0 {
			webhookPort = 8081
		}

		var whOpts []webhook.Option
		if cfg.Webhook.GitHub != nil {
			whOpts = append(whOpts, webhook.WithSecret("github", cfg.Webhook.GitHub.Secret))
		}
		if cfg.Webhook.GitLab != nil {
			whOpts = append(whOpts, webhook.WithSecret("gitlab", cfg.Webhook.GitLab.Secret))
		}
		if cfg.Webhook.Slack != nil {
			whOpts = append(whOpts, webhook.WithSecret("slack", cfg.Webhook.Slack.Secret))
		}
		if cfg.Webhook.Shortcut != nil {
			whOpts = append(whOpts, webhook.WithSecret("shortcut", cfg.Webhook.Shortcut.Secret))
		}
		if scBackend != nil && scBackend.WorkflowStateID() != 0 {
			whOpts = append(whOpts, webhook.WithShortcutTargetStateID(scBackend.WorkflowStateID()))
		}

		whHandler := &webhookAdapter{reconciler: reconciler, logger: logger}
		webhookSrv = webhook.NewServer(logger, whHandler, whOpts...)
		webhookAddr := fmt.Sprintf(":%d", webhookPort)
		go func() {
			logger.Info("starting webhook server", "addr", webhookAddr)
			if err := webhookSrv.ListenAndServe(webhookAddr); err != nil && err != http.ErrServerClosed {
				logger.Error("webhook server failed", "error", err)
			}
		}()
		logger.Info("webhook receiver enabled", "port", webhookPort)
	}

	// Mark as ready.
	ready.Store(true)
	logger.Info("controller initialised and ready")

	// Run the reconciliation loop.
	if err := reconciler.Run(ctx, *pollInterval); err != nil && err != context.Canceled {
		logger.Error("reconciler exited with error", "error", err)
		os.Exit(1)
	}

	// Gracefully shut down HTTP servers.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown error", "error", err)
	}
	if webhookSrv != nil {
		if err := webhookSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("webhook server shutdown error", "error", err)
		}
	}

	// Close memory store if initialised.
	if memoryStore != nil {
		if err := memoryStore.Close(); err != nil {
			logger.Error("memory store close error", "error", err)
		}
	}

	logger.Info("robodev controller stopped")
}

// buildK8sClient creates a Kubernetes clientset, trying in-cluster config
// first and falling back to kubeconfig for local development.
func buildK8sClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig (local dev).
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		cfg, err = kubeConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig: %w", err)
		}
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return client, nil
}

// readSecretValue reads a single key from a Kubernetes Secret.
func readSecretValue(ctx context.Context, client kubernetes.Interface, namespace, secretName, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	secret, err := client.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading secret %q: %w", secretName, err)
	}

	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", key, secretName)
	}
	return string(val), nil
}

// configString extracts a string value from a map[string]any config map.
func configString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("missing config key %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("config key %q is not a string", key)
	}
	return s, nil
}

// configStringOptional extracts a string value from a map[string]any config
// map, returning ("", false, nil) when the key is absent.
func configStringOptional(m map[string]any, key string) (string, bool, error) {
	v, ok := m[key]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fmt.Errorf("config key %q is not a string", key)
	}
	return s, true, nil
}

// configStringSlice extracts a []string from a map[string]any config map.
func configStringSlice(m map[string]any, key string) ([]string, error) {
	v, ok := m[key]
	if !ok {
		return nil, nil // optional
	}

	switch val := v.(type) {
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("config key %q contains non-string element", key)
			}
			result = append(result, s)
		}
		return result, nil
	case []string:
		return val, nil
	default:
		return nil, fmt.Errorf("config key %q is not a string slice", key)
	}
}

// initGitHubBackend creates and returns a GitHub ticketing backend from
// the controller configuration.
func initGitHubBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*ghticket.GitHubBackend, error) {
	m := cfg.Ticketing.Config

	owner, err := configString(m, "owner")
	if err != nil {
		return nil, err
	}
	repo, err := configString(m, "repo")
	if err != nil {
		return nil, err
	}
	labels, err := configStringSlice(m, "labels")
	if err != nil {
		return nil, err
	}
	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	token, err := readSecretValue(context.Background(), k8sClient, namespace, tokenSecret, "token")
	if err != nil {
		return nil, fmt.Errorf("reading github token: %w", err)
	}

	var opts []ghticket.Option

	if assignee, ok, err := configStringOptional(m, "assignee"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, ghticket.WithAssignee(assignee))
	}

	if milestone, ok, err := configStringOptional(m, "milestone"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, ghticket.WithMilestone(milestone))
	}

	if state, ok, err := configStringOptional(m, "state"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, ghticket.WithState(state))
	}

	excludeLabels, err := configStringSlice(m, "exclude_labels")
	if err != nil {
		return nil, err
	}
	if excludeLabels != nil {
		opts = append(opts, ghticket.WithExcludeLabels(excludeLabels))
	}

	return ghticket.NewGitHubBackend(owner, repo, labels, token, logger, opts...), nil
}

// initShortcutBackend creates and returns a Shortcut ticketing backend from
// the controller configuration. It calls Init to resolve human-readable
// state names and owner mention names to their Shortcut API identifiers.
func initShortcutBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*scticket.ShortcutBackend, error) {
	m := cfg.Ticketing.Config

	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	token, err := readSecretValue(context.Background(), k8sClient, namespace, tokenSecret, "token")
	if err != nil {
		return nil, fmt.Errorf("reading shortcut token: %w", err)
	}

	var opts []scticket.Option

	if stateName, ok, err := configStringOptional(m, "workflow_state_name"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, scticket.WithWorkflowStateName(stateName))
	}

	if inProgressName, ok, err := configStringOptional(m, "in_progress_state_name"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, scticket.WithInProgressStateName(inProgressName))
	}

	if ownerName, ok, err := configStringOptional(m, "owner_mention_name"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, scticket.WithOwnerMentionName(ownerName))
	}

	if excludeLabels, err := configStringSlice(m, "exclude_labels"); err != nil {
		return nil, err
	} else if len(excludeLabels) > 0 {
		opts = append(opts, scticket.WithExcludeLabels(excludeLabels))
	}

	// workflowStateID of 0 is valid here — Init will resolve it from
	// workflow_state_name. If no name is given either, PollReadyTickets will
	// return an error with a helpful message.
	backend := scticket.NewShortcutBackend(token, 0, logger, opts...)
	if err := backend.Init(context.Background()); err != nil {
		return nil, fmt.Errorf("initialising shortcut backend: %w", err)
	}

	return backend, nil
}

// webhookAdapter wraps the controller's Reconciler to satisfy the
// webhook.EventHandler interface, bridging webhook events into the
// controller's ticket processing pipeline.
type webhookAdapter struct {
	reconciler *controller.Reconciler
	logger     *slog.Logger
}

// HandleWebhookEvent feeds parsed webhook tickets into the controller.
func (a *webhookAdapter) HandleWebhookEvent(ctx context.Context, source string, tickets []ticketing.Ticket) error {
	a.logger.Info("processing webhook event",
		"source", source,
		"ticket_count", len(tickets),
	)
	for i := range tickets {
		if err := a.reconciler.ProcessTicket(ctx, tickets[i]); err != nil {
			a.logger.Error("failed to process webhook ticket",
				"source", source,
				"ticket_id", tickets[i].ID,
				"error", err,
			)
			// Continue processing remaining tickets.
		}
	}
	return nil
}

// initSlackChannel creates and returns a Slack notification channel from
// a channel configuration block.
func initSlackChannel(chCfg config.ChannelConfig, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*slacknotify.SlackChannel, error) {
	m := chCfg.Config

	channelID, err := configString(m, "channel_id")
	if err != nil {
		return nil, err
	}
	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	token, err := readSecretValue(context.Background(), k8sClient, namespace, tokenSecret, "token")
	if err != nil {
		return nil, fmt.Errorf("reading slack token: %w", err)
	}

	return slacknotify.NewSlackChannel(channelID, token, logger), nil
}
