// Package main is the entrypoint for the Osmia controller binary.
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

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/diagnosis"
	"github.com/unitaryai/osmia/internal/estimator"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/localui"
	"github.com/unitaryai/osmia/internal/memory"
	"github.com/unitaryai/osmia/internal/prm"
	"github.com/unitaryai/osmia/internal/reviewpoller"
	"github.com/unitaryai/osmia/internal/routing"
	"github.com/unitaryai/osmia/internal/sandboxbuilder"
	"github.com/unitaryai/osmia/internal/scmrouter"
	"github.com/unitaryai/osmia/internal/secretresolver"
	"github.com/unitaryai/osmia/internal/sessionstore"
	"github.com/unitaryai/osmia/internal/tournament"
	"github.com/unitaryai/osmia/internal/watchdog"
	"github.com/unitaryai/osmia/internal/webhook"
	"github.com/unitaryai/osmia/pkg/plugin/transcript/local"

	// Execution engines.
	"github.com/unitaryai/osmia/pkg/engine/aider"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/engine/cline"
	"github.com/unitaryai/osmia/pkg/engine/codex"
	"github.com/unitaryai/osmia/pkg/engine/opencode"

	// Ticketing backends.
	ghticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/github"
	linearticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/linear"
	localticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/local"
	noopticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/noop"
	scticket "github.com/unitaryai/osmia/pkg/plugin/ticketing/shortcut"

	// Notification backends.
	discordnotify "github.com/unitaryai/osmia/pkg/plugin/notifications/discord"
	slacknotify "github.com/unitaryai/osmia/pkg/plugin/notifications/slack"
	telegramnotify "github.com/unitaryai/osmia/pkg/plugin/notifications/telegram"

	// Repo URL polling.
	slackrepopoller "github.com/unitaryai/osmia/pkg/plugin/repourlpoller/slack"

	// Approval backend.
	approvalPkg "github.com/unitaryai/osmia/pkg/plugin/approval"
	slackapproval "github.com/unitaryai/osmia/pkg/plugin/approval/slack"

	// SCM backends.
	scmPkg "github.com/unitaryai/osmia/pkg/plugin/scm"
	ghscm "github.com/unitaryai/osmia/pkg/plugin/scm/github"
	glscm "github.com/unitaryai/osmia/pkg/plugin/scm/gitlab"

	// Secrets backends.
	awssmsecrets "github.com/unitaryai/osmia/pkg/plugin/secrets/awssm"
	k8ssecrets "github.com/unitaryai/osmia/pkg/plugin/secrets/k8s"
	vaultsecrets "github.com/unitaryai/osmia/pkg/plugin/secrets/vault"

	// Review backend.
	reviewPkg "github.com/unitaryai/osmia/pkg/plugin/review"
	crreview "github.com/unitaryai/osmia/pkg/plugin/review/coderabbit"

	// Webhook event bridge.
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"

	// Register metrics with the default Prometheus registry.
	_ "github.com/unitaryai/osmia/internal/metrics"
)

func main() {
	var (
		configPath   = flag.String("config", "/etc/osmia/config.yaml", "path to the Osmia configuration file")
		localUIAddr  = flag.String("local-ui-addr", "127.0.0.1:8082", "address for the local ticketing UI when ticketing.backend=local")
		metricsAddr  = flag.String("metrics-addr", ":8080", "address for the Prometheus metrics and health endpoints")
		pollInterval = flag.Duration("poll-interval", 30*time.Second, "interval between ticketing backend polls")
		namespace    = flag.String("namespace", "osmia", "kubernetes namespace for job creation")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("starting osmia controller",
		"config", *configPath,
		"local_ui_addr", *localUIAddr,
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
	k8sClient, restCfg, err := buildK8sClient()
	if err != nil {
		logger.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}
	logger.Info("kubernetes client initialised")

	// Collect reconciler options.
	opts := []controller.ReconcilerOption{
		controller.WithNamespace(*namespace),
		controller.WithK8sClient(k8sClient),
		controller.WithRestConfig(restCfg),
	}

	// --- Ticketing backend ---
	var localBackend *localticket.Backend
	var scBackend *scticket.ShortcutBackend
	if cfg.Ticketing.Backend == "github" {
		ghBackend, ghErr := initGitHubBackend(cfg, k8sClient, *namespace, logger)
		if ghErr != nil {
			logger.Error("failed to initialise github ticketing backend", "error", ghErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithTicketing(ghBackend))
		logger.Info("github ticketing backend initialised")
	} else if cfg.Ticketing.Backend == "linear" {
		linearBackend, linearErr := initLinearBackend(cfg, k8sClient, *namespace, logger)
		if linearErr != nil {
			logger.Error("failed to initialise linear ticketing backend", "error", linearErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithTicketing(linearBackend))
		logger.Info("linear ticketing backend initialised")
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
	} else if cfg.Ticketing.Backend == "local" {
		var localErr error
		localBackend, localErr = initLocalBackend(cfg, logger)
		if localErr != nil {
			logger.Error("failed to initialise local ticketing backend", "error", localErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithTicketing(localBackend))
		logger.Info("local ticketing backend initialised")
	} else if cfg.Ticketing.Backend != "" {
		logger.Error("unsupported ticketing backend", "backend", cfg.Ticketing.Backend)
		os.Exit(1)
	} else {
		if taskFile, ok, err := configStringOptional(cfg.Ticketing.Config, "task_file"); err != nil {
			logger.Error("invalid task_file config", "error", err)
			os.Exit(1)
		} else if ok && taskFile != "" {
			logger.Error("ticketing.config.task_file is no longer supported; use ticketing.backend=local with ticketing.config.store_path and optional ticketing.config.seed_file")
			os.Exit(1)
		} else {
			opts = append(opts, controller.WithTicketing(noopticket.New()))
			logger.Info("no ticketing backend configured, using noop fallback")
		}
	}

	// --- Execution engines ---
	var claudeOpts []claudecode.Option
	if cfg.Engines.ClaudeCode != nil && len(cfg.Engines.ClaudeCode.Skills) > 0 {
		skills := make([]claudecode.Skill, 0, len(cfg.Engines.ClaudeCode.Skills))
		for _, sc := range cfg.Engines.ClaudeCode.Skills {
			skills = append(skills, claudecode.Skill{
				Name:      sc.Name,
				Path:      sc.Path,
				Inline:    sc.Inline,
				ConfigMap: sc.ConfigMap,
				Key:       sc.Key,
			})
		}
		claudeOpts = append(claudeOpts, claudecode.WithSkills(skills))
		logger.Info("claude-code skills configured", "count", len(skills))
	}
	if cfg.Engines.ClaudeCode != nil && len(cfg.Engines.ClaudeCode.SubAgents) > 0 {
		subAgents := make([]claudecode.SubAgent, 0, len(cfg.Engines.ClaudeCode.SubAgents))
		for _, sa := range cfg.Engines.ClaudeCode.SubAgents {
			subAgents = append(subAgents, claudecode.SubAgent{
				Name:            sa.Name,
				Description:     sa.Description,
				Prompt:          sa.Prompt,
				Model:           sa.Model,
				Tools:           sa.Tools,
				DisallowedTools: sa.DisallowedTools,
				PermissionMode:  sa.PermissionMode,
				MaxTurns:        sa.MaxTurns,
				Skills:          sa.Skills,
				Background:      sa.Background,
				ConfigMap:       sa.ConfigMap,
				Key:             sa.Key,
			})
		}
		claudeOpts = append(claudeOpts, claudecode.WithSubAgents(subAgents))
		logger.Info("claude-code sub-agents configured", "count", len(subAgents))
	}
	if cfg.Engines.ClaudeCode != nil && cfg.Engines.ClaudeCode.MaxTurns > 0 {
		claudeOpts = append(claudeOpts, claudecode.WithMaxTurns(cfg.Engines.ClaudeCode.MaxTurns))
		logger.Info("claude-code max turns configured", "max_turns", cfg.Engines.ClaudeCode.MaxTurns)
	}
	if cfg.Engines.ClaudeCode != nil && cfg.Engines.ClaudeCode.AgentTeams.Enabled {
		claudeOpts = append(claudeOpts, claudecode.WithTeamsConfig(claudecode.TeamsConfig{
			Enabled:       true,
			Mode:          cfg.Engines.ClaudeCode.AgentTeams.Mode,
			MaxTeammates:  cfg.Engines.ClaudeCode.AgentTeams.MaxTeammates,
			TeammateModel: cfg.Engines.ClaudeCode.AgentTeams.TeammateModel,
		}))
		logger.Info("claude-code agent teams enabled",
			"mode", cfg.Engines.ClaudeCode.AgentTeams.Mode,
			"max_teammates", cfg.Engines.ClaudeCode.AgentTeams.MaxTeammates,
		)
	}
	// --- Session persistence (Claude Code only) ---
	var sessionCleaner *sessionstore.Cleaner
	if cfg.Engines.ClaudeCode != nil && cfg.Engines.ClaudeCode.SessionPersistence.Enabled {
		sp := cfg.Engines.ClaudeCode.SessionPersistence
		switch sp.Backend {
		case "shared-pvc":
			store := sessionstore.NewSharedPVCStore(sp.PVCName)
			claudeOpts = append(claudeOpts, claudecode.WithSessionStore(store))
			opts = append(opts, controller.WithSessionStore(store))
			sessionCleaner = sessionstore.NewCleaner(sessionstore.CleanerConfig{
				Backend:    "shared-pvc",
				PVCRootDir: "/data/sessions",
				TTL:        time.Duration(sp.TTLMinutes) * time.Minute,
			}, logger.With("component", "session-cleaner"))
			logger.Info("session persistence enabled",
				"backend", "shared-pvc",
				"pvc_name", sp.PVCName,
				"ttl_minutes", sp.TTLMinutes,
			)
		case "per-taskrun-pvc":
			store := sessionstore.NewPerTaskRunPVCStore(
				k8sClient, *namespace,
				sp.StorageClass, sp.StorageSize,
				logger.With("component", "session-store"),
			)
			claudeOpts = append(claudeOpts, claudecode.WithSessionStore(store))
			opts = append(opts, controller.WithSessionStore(store))
			sessionCleaner = sessionstore.NewCleaner(sessionstore.CleanerConfig{
				Backend:   "per-taskrun-pvc",
				K8sClient: k8sClient,
				Namespace: *namespace,
				TTL:       time.Duration(sp.TTLMinutes) * time.Minute,
			}, logger.With("component", "session-cleaner"))
			logger.Info("session persistence enabled",
				"backend", "per-taskrun-pvc",
				"storage_class", sp.StorageClass,
				"storage_size", sp.StorageSize,
				"ttl_minutes", sp.TTLMinutes,
			)
		default:
			logger.Error("unsupported session persistence backend", "backend", sp.Backend)
			os.Exit(1)
		}
	}

	claudeEngine := claudecode.New(claudeOpts...)
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

	if cfg.Engines.Aider != nil {
		aiderEngine := aider.New()
		opts = append(opts, controller.WithEngine(aiderEngine))
		logger.Info("aider engine registered")
	}

	if cfg.Engines.Codex != nil {
		codexEngine := codex.New()
		opts = append(opts, controller.WithEngine(codexEngine))
		logger.Info("codex engine registered")
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
		switch chCfg.Backend {
		case "slack":
			slackCh, repoPoller, slackErr := initSlackChannel(chCfg, k8sClient, *namespace, logger)
			if slackErr != nil {
				logger.Warn("failed to initialise slack notifications, continuing without",
					"error", slackErr,
				)
				continue
			}
			opts = append(opts, controller.WithNotifier(slackCh))
			opts = append(opts, controller.WithRepoURLPoller(repoPoller))
			logger.Info("slack notification channel initialised")
		case "discord":
			discordCh, discordErr := initDiscordChannel(chCfg, logger)
			if discordErr != nil {
				logger.Warn("failed to initialise discord notifications, continuing without",
					"error", discordErr,
				)
				continue
			}
			opts = append(opts, controller.WithNotifier(discordCh))
			logger.Info("discord notification channel initialised")
		case "telegram":
			telegramCh, telegramErr := initTelegramChannel(chCfg, k8sClient, *namespace, logger)
			if telegramErr != nil {
				logger.Warn("failed to initialise telegram notifications, continuing without",
					"error", telegramErr,
				)
				continue
			}
			opts = append(opts, controller.WithNotifier(telegramCh))
			logger.Info("telegram notification channel initialised")
		default:
			logger.Warn("unknown notification backend, skipping", "backend", chCfg.Backend)
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

	// --- Session cleaner (background) ---
	if sessionCleaner != nil {
		go sessionCleaner.Run(ctx)
		logger.Info("session cleaner started")
	}

	// --- Approval backend (non-critical) ---
	if cfg.Approval.Backend != "" {
		approvalBackend, approvalErr := initApprovalBackend(cfg, k8sClient, *namespace, logger)
		if approvalErr != nil {
			logger.Warn("failed to initialise approval backend, continuing without",
				"backend", cfg.Approval.Backend,
				"error", approvalErr,
			)
		} else {
			opts = append(opts, controller.WithApprovalBackend(approvalBackend))
			logger.Info("approval backend initialised", "backend", cfg.Approval.Backend)
		}
	}

	// --- SCM backend(s) ---
	// Multi-backend router takes precedence over the legacy single-backend config.
	if len(cfg.SCM.Backends) > 0 {
		var entries []scmrouter.Entry
		for _, be := range cfg.SCM.Backends {
			beCfg := &config.Config{SCM: config.SCMConfig{Backend: be.Backend, Config: be.Config}}
			backend, beErr := initSCMBackend(beCfg, k8sClient, *namespace, logger)
			if beErr != nil {
				logger.Warn("failed to initialise SCM backend in router, skipping",
					"backend", be.Backend,
					"match", be.Match,
					"error", beErr,
				)
				continue
			}
			entries = append(entries, scmrouter.Entry{Match: be.Match, Backend: backend})
			logger.Info("SCM backend registered in router",
				"backend", be.Backend,
				"match", be.Match,
			)
		}
		if len(entries) > 0 {
			opts = append(opts, controller.WithSCMRouter(scmrouter.NewRouter(entries...)))
			logger.Info("SCM router initialised", "backend_count", len(entries))
		}
	} else if cfg.SCM.Backend != "" {
		scmBackend, scmErr := initSCMBackend(cfg, k8sClient, *namespace, logger)
		if scmErr != nil {
			logger.Warn("failed to initialise SCM backend, continuing without",
				"backend", cfg.SCM.Backend,
				"error", scmErr,
			)
		} else {
			opts = append(opts, controller.WithSCMBackend(scmBackend))
			logger.Info("SCM backend initialised", "backend", cfg.SCM.Backend)
		}
	}

	// --- Review backend (non-critical) ---
	if cfg.Review.Backend != "" {
		reviewBackend, reviewErr := initReviewBackend(cfg, k8sClient, *namespace, logger)
		if reviewErr != nil {
			logger.Warn("failed to initialise review backend, continuing without",
				"backend", cfg.Review.Backend,
				"error", reviewErr,
			)
		} else {
			opts = append(opts, controller.WithReviewBackend(reviewBackend))
			logger.Info("review backend initialised", "backend", cfg.Review.Backend)
		}
	}

	// --- Secrets resolver ---
	if len(cfg.SecretResolver.Backends) > 0 {
		sr, srErr := initSecretsResolver(cfg, k8sClient, *namespace, logger)
		if srErr != nil {
			logger.Error("failed to initialise secrets resolver", "error", srErr)
			os.Exit(1)
		}
		opts = append(opts, controller.WithSecretsResolver(sr))
		logger.Info("secrets resolver initialised",
			"backend_count", len(cfg.SecretResolver.Backends),
		)
	}

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

	// --- Diagnosis (causal failure classification + enriched retry) ---
	if cfg.Diagnosis.Enabled {
		diagAnalyser := diagnosis.NewAnalyser(logger.With("component", "diagnosis"))
		diagRetryBuilder := diagnosis.NewRetryBuilder(logger.With("component", "retry-builder"))
		opts = append(opts, controller.WithDiagnosis(diagAnalyser, diagRetryBuilder))
		logger.Info("diagnosis subsystem enabled",
			"max_diagnoses_per_task", cfg.Diagnosis.MaxDiagnosesPerTask,
			"engine_switch_enabled", cfg.Diagnosis.EnableEngineSwitch,
		)
	}

	// --- Watchdog + adaptive calibration ---
	if cfg.ProgressWatchdog.Enabled {
		wdCfg := watchdog.Config{
			CheckIntervalSeconds:       cfg.ProgressWatchdog.CheckIntervalSeconds,
			MinConsecutiveTicks:        cfg.ProgressWatchdog.MinConsecutiveTicks,
			ResearchGracePeriodMinutes: cfg.ProgressWatchdog.ResearchGracePeriodMinutes,
			Rules: watchdog.RulesConfig{
				LoopDetection: watchdog.LoopDetectionConfig{
					ConsecutiveIdenticalCallThreshold: cfg.ProgressWatchdog.LoopDetectionThreshold,
					RequireNoFileProgress:             true,
					Action:                            watchdog.ActionTerminateWithFeedback,
				},
				ThrashingDetection: watchdog.ThrashingDetectionConfig{
					TokensWithoutProgressThreshold: int64(cfg.ProgressWatchdog.ThrashingTokenThreshold),
					Action:                         watchdog.ActionWarn,
					EscalationAction:               watchdog.ActionTerminateWithFeedback,
				},
				StallDetection: watchdog.StallDetectionConfig{
					IdleSecondsThreshold: cfg.ProgressWatchdog.StallIdleSeconds,
					Action:               watchdog.ActionTerminate,
				},
				CostVelocity: watchdog.CostVelocityConfig{
					MaxUSDPer10Minutes: cfg.ProgressWatchdog.CostVelocityMaxPer10Min,
					Action:             watchdog.ActionWarn,
				},
				MaxCostPerJob:                 cfg.GuardRails.MaxCostPerJob,
				UnansweredHumanTimeoutMinutes: cfg.ProgressWatchdog.UnansweredHumanTimeoutMin,
				UnansweredHumanAction:         watchdog.ActionTerminateAndNotify,
			},
			AdaptiveCalibration: watchdog.AdaptiveCalibrationConfig{
				Enabled:             cfg.ProgressWatchdog.AdaptiveCalibration.Enabled,
				MinSampleCount:      cfg.ProgressWatchdog.AdaptiveCalibration.MinSampleCount,
				PercentileThreshold: cfg.ProgressWatchdog.AdaptiveCalibration.PercentileThreshold,
				ColdStartFallback:   cfg.ProgressWatchdog.AdaptiveCalibration.ColdStartFallback,
			},
		}
		if wdCfg.CheckIntervalSeconds <= 0 {
			wdCfg.CheckIntervalSeconds = 60
		}
		if wdCfg.MinConsecutiveTicks <= 0 {
			wdCfg.MinConsecutiveTicks = 2
		}

		calibrator := watchdog.NewCalibrator(logger.With("component", "watchdog-calibrator"))
		profileStore := watchdog.NewMemoryProfileStore()
		minSamples := cfg.ProgressWatchdog.AdaptiveCalibration.MinSampleCount
		if minSamples <= 0 {
			minSamples = 10
		}
		profileResolver := watchdog.NewProfileResolver(profileStore, calibrator, minSamples)

		wd := watchdog.NewWithCalibration(wdCfg, logger.With("component", "watchdog"), calibrator, profileResolver)
		opts = append(opts, controller.WithWatchdog(wd))
		opts = append(opts, controller.WithWatchdogCalibration(calibrator, profileResolver))
		logger.Info("watchdog enabled",
			"check_interval_seconds", wdCfg.CheckIntervalSeconds,
			"adaptive_calibration", wdCfg.AdaptiveCalibration.Enabled,
		)
	}

	// --- Intelligent routing ---
	if cfg.Routing.Enabled {
		fingerprintStore := routing.NewMemoryFingerprintStore()
		// Only advertise engines that are genuinely configured. Ranging over a
		// map[string]bool and ignoring the value would include every key
		// regardless of whether the engine is actually enabled.
		configuredEngines := map[string]bool{
			"claude-code": cfg.Engines.ClaudeCode != nil || cfg.Engines.Default == "claude-code",
			"opencode":    cfg.Engines.OpenCode != nil,
			"cline":       cfg.Engines.Cline != nil,
			"aider":       cfg.Engines.Aider != nil,
			"codex":       cfg.Engines.Codex != nil,
		}
		var availableEngines []string
		for name, enabled := range configuredEngines {
			if enabled {
				availableEngines = append(availableEngines, name)
			}
		}
		// Always include the default engine even if it was not matched above.
		if cfg.Engines.Default != "" {
			found := false
			for _, e := range availableEngines {
				if e == cfg.Engines.Default {
					found = true
					break
				}
			}
			if !found {
				availableEngines = append(availableEngines, cfg.Engines.Default)
			}
		}
		sel := routing.NewIntelligentSelector(fingerprintStore, nil, &cfg.Routing, availableEngines, logger.With("component", "routing"))
		opts = append(opts, controller.WithIntelligentSelector(sel))
		logger.Info("intelligent routing enabled",
			"epsilon_greedy", cfg.Routing.EpsilonGreedy,
			"engine_count", len(availableEngines),
		)
	}

	// --- Estimator (predictive cost/duration) ---
	if cfg.Estimator.Enabled {
		estStore := estimator.NewMemoryEstimatorStore()
		predictor := estimator.NewPredictor(estStore, &cfg.Estimator, logger.With("component", "estimator"))
		scorer := estimator.NewComplexityScorer()
		opts = append(opts, controller.WithEstimator(predictor, scorer))
		logger.Info("estimator enabled",
			"max_predicted_cost_per_job_usd", cfg.Estimator.MaxPredictedCostPerJob,
		)
	}

	// --- Transcript storage (audit log) ---
	if cfg.Audit.Transcript.Backend == "local" && cfg.Audit.Transcript.Path != "" {
		transcriptSink := local.NewLocalSink(cfg.Audit.Transcript.Path, logger.With("component", "transcript"))
		opts = append(opts, controller.WithTranscriptSink(transcriptSink))
		logger.Info("transcript storage enabled",
			"backend", "local",
			"path", cfg.Audit.Transcript.Path,
		)
	}

	// --- Memory (cross-task episodic knowledge) ---
	var memoryStore *memory.SQLiteStore
	if cfg.Memory.Enabled {
		storePath := cfg.Memory.StorePath
		if storePath == "" {
			storePath = "/var/lib/osmia/memory.db"
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

	// --- Competitive execution (tournament coordinator) ---
	if cfg.CompetitiveExecution.Enabled {
		tc := tournament.NewCoordinator(logger.With("component", "tournament"))
		opts = append(opts, controller.WithTournamentCoordinator(tc))
		logger.Info("tournament coordinator enabled",
			"default_candidates", cfg.CompetitiveExecution.DefaultCandidates,
			"judge_engine", cfg.CompetitiveExecution.JudgeEngine,
		)
	}

	// --- Review response (PR/MR comment monitoring) ---
	if cfg.ReviewResponse.Enabled {
		classifier := reviewpoller.NewRuleBasedClassifier(cfg.ReviewResponse.IgnoreSummaryAuthors)
		poller := reviewpoller.New(cfg.ReviewResponse, classifier, logger.With("component", "review-poller"))
		if len(cfg.SCM.Backends) > 0 {
			// Re-use the already-constructed router from the SCM section above.
			// The router has been added to opts via WithSCMRouter; retrieve it
			// by examining the opts slice is not idiomatic, so we build a second
			// lightweight router for the poller here. The overhead is negligible
			// since no HTTP connections are established at construction time.
			var entries []scmrouter.Entry
			for _, be := range cfg.SCM.Backends {
				beCfg := &config.Config{SCM: config.SCMConfig{Backend: be.Backend, Config: be.Config}}
				backend, beErr := initSCMBackend(beCfg, k8sClient, *namespace, logger)
				if beErr == nil {
					entries = append(entries, scmrouter.Entry{Match: be.Match, Backend: backend})
				}
			}
			if len(entries) > 0 {
				poller.WithSCMRouter(scmrouter.NewRouter(entries...))
			}
		} else if cfg.SCM.Backend != "" {
			scmBackend, scmErr := initSCMBackend(cfg, k8sClient, *namespace, logger)
			if scmErr == nil {
				poller.WithSCMBackend(scmBackend)
			}
		}
		opts = append(opts, controller.WithReviewPoller(poller))
		go poller.Start(ctx)
		logger.Info("review response poller enabled",
			"poll_interval_minutes", cfg.ReviewResponse.PollIntervalMinutes,
			"min_severity", cfg.ReviewResponse.MinSeverity,
			"max_follow_up_jobs", cfg.ReviewResponse.MaxFollowUpJobs,
		)
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

	var localUISrv *http.Server
	if localBackend != nil {
		localUIHandler, uiErr := localui.NewHandler(logger.With("component", "local-ui"), localBackend)
		if uiErr != nil {
			logger.Error("failed to initialise local ticketing UI", "error", uiErr)
			os.Exit(1)
		}
		localUISrv = &http.Server{
			Addr:              *localUIAddr,
			Handler:           localUIHandler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		logger.Info("local ticketing UI enabled", "addr", *localUIAddr, "url", fmt.Sprintf("http://%s/", *localUIAddr))
	}

	// Start the HTTP server in a goroutine.
	go func() {
		logger.Info("starting metrics and health server", "addr", *metricsAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "error", err)
			os.Exit(1)
		}
	}()
	if localUISrv != nil {
		go func() {
			logger.Info("starting local ticketing UI server", "addr", localUISrv.Addr)
			if err := localUISrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("local ticketing UI server failed", "error", err)
				os.Exit(1)
			}
		}()
	}

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
			triggerLabels := cfg.Webhook.GitHub.TriggerLabels
			if len(triggerLabels) == 0 && cfg.Ticketing.Backend == "github" {
				if labels, err := configStringSlice(cfg.Ticketing.Config, "labels"); err == nil && len(labels) > 0 {
					triggerLabels = labels
					logger.Info("webhook trigger labels derived from ticketing config", "labels", triggerLabels)
				}
			}
			if len(triggerLabels) > 0 {
				whOpts = append(whOpts, webhook.WithGitHubTriggerLabels(triggerLabels))
			}
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

		approvalHandler := &approvalAdapter{reconciler: reconciler, logger: logger}
		whOpts = append(whOpts, webhook.WithApprovalHandler(approvalHandler))

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
	if localUISrv != nil {
		if err := localUISrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("local ticketing UI server shutdown error", "error", err)
		}
	}
	if localBackend != nil {
		if err := localBackend.Close(); err != nil {
			logger.Error("local ticketing backend close error", "error", err)
		}
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

	logger.Info("osmia controller stopped")
}

// buildK8sClient creates a Kubernetes clientset, trying in-cluster config
// first and falling back to kubeconfig for local development. It returns
// both the clientset and the underlying rest.Config (needed for pod exec).
func buildK8sClient() (kubernetes.Interface, *rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig (local dev).
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		cfg, err = kubeConfig.ClientConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("building kubeconfig: %w", err)
		}
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return client, cfg, nil
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

// resolveSecretToken reads a token from a Kubernetes secret by trying keys in
// priority order: an explicit token_key from the config map, then well-known
// key names for the specific backend, then the generic "token" fallback. This
// allows users to use a single shared secret with descriptive key names
// (e.g. SHORTCUT_API_TOKEN) or dedicated per-service secrets with a "token" key.
func resolveSecretToken(ctx context.Context, client kubernetes.Interface, namespace, secretName string, m map[string]any, wellKnownKeys ...string) (string, error) {
	// 1. Explicit override from config.
	if key, ok := m["token_key"].(string); ok && key != "" {
		return readSecretValue(ctx, client, namespace, secretName, key)
	}
	// 2. Try well-known keys for this backend.
	for _, key := range wellKnownKeys {
		val, err := readSecretValue(ctx, client, namespace, secretName, key)
		if err == nil {
			return val, nil
		}
	}
	// 3. Generic fallback.
	return readSecretValue(ctx, client, namespace, secretName, "token")
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

	token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "GITHUB_TOKEN")
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

	token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "SHORTCUT_API_TOKEN", "SHORTCUT_TOKEN")
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

	if completedName, ok, err := configStringOptional(m, "completed_state_name"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, scticket.WithCompletedStateName(completedName))
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

	// Multi-workflow support: the workflows array takes precedence over the
	// legacy flat workflow_state_name / in_progress_state_name keys.
	if workflowsRaw, ok := m["workflows"]; ok {
		workflows, ok := workflowsRaw.([]interface{})
		if !ok {
			return nil, fmt.Errorf("shortcut workflows must be a list")
		}
		var mappings []scticket.WorkflowMapping
		for i, wfRaw := range workflows {
			wf, ok := wfRaw.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("shortcut workflow entry %d must be a map", i)
			}
			triggerState, _ := wf["trigger_state"].(string)
			inProgressState, _ := wf["in_progress_state"].(string)
			if triggerState == "" {
				return nil, fmt.Errorf("shortcut workflow entry %d missing trigger_state", i)
			}
			mappings = append(mappings, scticket.WorkflowMapping{
				TriggerState:    triggerState,
				InProgressState: inProgressState,
			})
		}
		if len(mappings) > 0 {
			opts = append(opts, scticket.WithWorkflowMappings(mappings))
		}
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

// initLocalBackend creates and returns a local SQLite-backed ticketing backend
// from the controller configuration.
func initLocalBackend(cfg *config.Config, logger *slog.Logger) (*localticket.Backend, error) {
	m := cfg.Ticketing.Config

	storePath, err := configString(m, "store_path")
	if err != nil {
		return nil, err
	}
	seedFile, _, err := configStringOptional(m, "seed_file")
	if err != nil {
		return nil, err
	}

	return localticket.New(localticket.Config{
		StorePath: storePath,
		SeedFile:  seedFile,
	}, logger)
}

// webhookAdapter wraps the controller's Reconciler to satisfy the
// webhook.EventHandler interface, bridging webhook events into the
// controller's ticket processing pipeline.
type webhookAdapter struct {
	reconciler *controller.Reconciler
	logger     *slog.Logger
}

// HandleWebhookEvent feeds parsed webhook tickets into the controller.
// An error is returned if any ticket fails to process so that the webhook
// server responds with a non-2xx status and the sender can retry.
func (a *webhookAdapter) HandleWebhookEvent(ctx context.Context, source string, tickets []ticketing.Ticket) error {
	a.logger.Info("processing webhook event",
		"source", source,
		"ticket_count", len(tickets),
	)
	var firstErr error
	for i := range tickets {
		if err := a.reconciler.ProcessTicket(ctx, tickets[i]); err != nil {
			a.logger.Error("failed to process webhook ticket",
				"source", source,
				"ticket_id", tickets[i].ID,
				"error", err,
			)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// approvalAdapter wraps the controller's Reconciler to satisfy the
// webhook.ApprovalHandler interface, bridging approval callbacks from
// webhooks into the controller's approval resolution logic.
type approvalAdapter struct {
	reconciler *controller.Reconciler
	logger     *slog.Logger
}

// HandleApprovalCallback delegates to the controller's ResolveApproval method.
func (a *approvalAdapter) HandleApprovalCallback(ctx context.Context, taskRunID string, approved bool, responder string) error {
	return a.reconciler.ResolveApproval(ctx, taskRunID, approved, responder)
}

// initLinearBackend creates and returns a Linear ticketing backend from the
// controller configuration.
func initLinearBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*linearticket.LinearBackend, error) {
	m := cfg.Ticketing.Config

	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "LINEAR_API_KEY", "LINEAR_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("reading linear token: %w", err)
	}

	teamID, err := configString(m, "team_id")
	if err != nil {
		return nil, err
	}

	var opts []linearticket.Option

	if state, ok, err := configStringOptional(m, "state_filter"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, linearticket.WithStateFilter(state))
	}

	if labels, err := configStringSlice(m, "labels"); err != nil {
		return nil, err
	} else if len(labels) > 0 {
		opts = append(opts, linearticket.WithLabels(labels))
	}

	if excludeLabels, err := configStringSlice(m, "exclude_labels"); err != nil {
		return nil, err
	} else if len(excludeLabels) > 0 {
		opts = append(opts, linearticket.WithExcludeLabels(excludeLabels))
	}

	return linearticket.NewLinearBackend(token, teamID, logger, opts...), nil
}

// initDiscordChannel creates and returns a Discord notification channel from
// a channel configuration block.
func initDiscordChannel(chCfg config.ChannelConfig, logger *slog.Logger) (*discordnotify.DiscordChannel, error) {
	m := chCfg.Config

	webhookURL, err := configString(m, "webhook_url")
	if err != nil {
		return nil, err
	}

	return discordnotify.New(webhookURL), nil
}

// initTelegramChannel creates and returns a Telegram notification channel from
// a channel configuration block.
func initTelegramChannel(chCfg config.ChannelConfig, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*telegramnotify.TelegramChannel, error) {
	m := chCfg.Config

	chatID, err := configString(m, "chat_id")
	if err != nil {
		return nil, err
	}
	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "TELEGRAM_BOT_TOKEN", "TELEGRAM_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("reading telegram token: %w", err)
	}

	var opts []telegramnotify.Option

	if rawThreadID, ok, err := configStringOptional(m, "thread_id"); err != nil {
		return nil, err
	} else if ok {
		var threadID int
		if _, scanErr := fmt.Sscan(rawThreadID, &threadID); scanErr != nil {
			return nil, fmt.Errorf("config key \"thread_id\" is not a valid integer: %w", scanErr)
		}
		opts = append(opts, telegramnotify.WithThreadID(threadID))
	}

	return telegramnotify.New(token, chatID, opts...), nil
}

// initApprovalBackend creates and returns a human approval backend from the
// controller configuration.
func initApprovalBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (approvalPkg.Backend, error) {
	m := cfg.Approval.Config

	switch cfg.Approval.Backend {
	case "slack":
		channelID, err := configString(m, "channel_id")
		if err != nil {
			return nil, err
		}
		tokenSecret, err := configString(m, "token_secret")
		if err != nil {
			return nil, err
		}
		token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "SLACK_BOT_TOKEN", "SLACK_TOKEN")
		if err != nil {
			return nil, fmt.Errorf("reading slack approval token: %w", err)
		}
		return slackapproval.NewSlackApprovalBackend(channelID, token, logger), nil
	default:
		return nil, fmt.Errorf("unsupported approval backend %q", cfg.Approval.Backend)
	}
}

// initSCMBackend creates and returns an SCM backend from the controller
// configuration.
func initSCMBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (scmPkg.Backend, error) {
	m := cfg.SCM.Config

	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, err
	}

	switch cfg.SCM.Backend {
	case "github":
		token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "GITHUB_TOKEN")
		if err != nil {
			return nil, fmt.Errorf("reading github SCM token: %w", err)
		}
		return ghscm.NewGitHubSCMBackend(token, logger), nil
	case "gitlab":
		token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "GITLAB_TOKEN")
		if err != nil {
			return nil, fmt.Errorf("reading gitlab SCM token: %w", err)
		}
		var opts []glscm.Option
		if baseURL, ok, err := configStringOptional(m, "base_url"); err != nil {
			return nil, err
		} else if ok {
			opts = append(opts, glscm.WithBaseURL(baseURL))
		}
		return glscm.NewGitLabSCMBackend(token, logger, opts...), nil
	default:
		return nil, fmt.Errorf("unsupported SCM backend %q", cfg.SCM.Backend)
	}
}

// initReviewBackend creates and returns a code review backend from the
// controller configuration.
func initReviewBackend(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (reviewPkg.Backend, error) {
	m := cfg.Review.Config

	switch cfg.Review.Backend {
	case "coderabbit":
		apiKeySecret, err := configString(m, "api_key_secret")
		if err != nil {
			return nil, err
		}
		apiKey, err := resolveSecretToken(context.Background(), k8sClient, namespace, apiKeySecret, m, "CODERABBIT_API_KEY", "api_key")
		if err != nil {
			return nil, fmt.Errorf("reading coderabbit api key: %w", err)
		}
		return crreview.NewCodeRabbitBackend(apiKey, logger), nil
	default:
		return nil, fmt.Errorf("unsupported review backend %q", cfg.Review.Backend)
	}
}

// initSecretsResolver creates and returns a secrets resolver populated with
// the backends declared in cfg.SecretResolver.Backends.
func initSecretsResolver(cfg *config.Config, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*secretresolver.Resolver, error) {
	var opts []secretresolver.Option

	for _, ref := range cfg.SecretResolver.Backends {
		switch ref.Backend {
		case "k8s":
			backend := k8ssecrets.NewK8sBackend(namespace, k8sClient, logger)
			opts = append(opts, secretresolver.WithBackend(ref.Scheme, backend))
		case "vault":
			var vaultOpts []vaultsecrets.VaultOption
			if address, ok := ref.Config["address"].(string); ok && address != "" {
				vaultOpts = append(vaultOpts, vaultsecrets.WithAddress(address))
			}
			if role, ok := ref.Config["role"].(string); ok && role != "" {
				vaultOpts = append(vaultOpts, vaultsecrets.WithRole(role))
			}
			if method, ok := ref.Config["auth_method"].(string); ok && method != "" {
				vaultOpts = append(vaultOpts, vaultsecrets.WithAuthMethod(method))
			}
			if path, ok := ref.Config["secrets_path"].(string); ok && path != "" {
				vaultOpts = append(vaultOpts, vaultsecrets.WithSecretsPath(path))
			}
			vaultOpts = append(vaultOpts, vaultsecrets.WithLogger(logger))
			backend := vaultsecrets.NewVaultBackend(vaultOpts...)
			opts = append(opts, secretresolver.WithBackend(ref.Scheme, backend))
		case "aws-secrets-manager":
			var awssmOpts []awssmsecrets.Option
			if region, ok := ref.Config["region"].(string); ok && region != "" {
				awssmOpts = append(awssmOpts, awssmsecrets.WithRegion(region))
			}
			if roleARN, ok := ref.Config["assume_role_arn"].(string); ok && roleARN != "" {
				awssmOpts = append(awssmOpts, awssmsecrets.WithAssumeRoleARN(roleARN))
			}
			if ttlStr, ok := ref.Config["cache_ttl"].(string); ok && ttlStr != "" {
				ttl, err := time.ParseDuration(ttlStr)
				if err != nil {
					return nil, fmt.Errorf("invalid cache_ttl %q for aws-secrets-manager: %w", ttlStr, err)
				}
				awssmOpts = append(awssmOpts, awssmsecrets.WithCacheTTL(ttl))
			}
			awssmOpts = append(awssmOpts, awssmsecrets.WithLogger(logger))
			backend := awssmsecrets.NewBackend(awssmOpts...)
			opts = append(opts, secretresolver.WithBackend(ref.Scheme, backend))
		default:
			return nil, fmt.Errorf("unsupported secret backend type %q (scheme %q)", ref.Backend, ref.Scheme)
		}
	}

	// Always include a logger.
	opts = append(opts, secretresolver.WithLogger(logger))

	// Wire policy from config.
	policy := secretresolver.Policy{
		AllowRawRefs:       cfg.SecretResolver.Policy.AllowRawRefs,
		AllowedEnvPatterns: cfg.SecretResolver.Policy.AllowedEnvPatterns,
		BlockedEnvPatterns: cfg.SecretResolver.Policy.BlockedEnvPatterns,
		AllowedSchemes:     cfg.SecretResolver.Policy.AllowedSchemes,
	}
	opts = append(opts, secretresolver.WithPolicy(policy))

	return secretresolver.NewResolver(opts...), nil
}

// initSlackChannel creates and returns a Slack notification channel and a
// RepoURLPoller from a channel configuration block. The bot token stays
// inside the plugin layer — callers never see it.
func initSlackChannel(chCfg config.ChannelConfig, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) (*slacknotify.SlackChannel, *slackrepopoller.Poller, error) {
	m := chCfg.Config

	channelID, err := configString(m, "channel_id")
	if err != nil {
		return nil, nil, err
	}
	tokenSecret, err := configString(m, "token_secret")
	if err != nil {
		return nil, nil, err
	}

	token, err := resolveSecretToken(context.Background(), k8sClient, namespace, tokenSecret, m, "SLACK_BOT_TOKEN", "SLACK_TOKEN")
	if err != nil {
		return nil, nil, fmt.Errorf("reading slack token: %w", err)
	}

	notifier := slacknotify.NewSlackChannel(channelID, token, logger)
	poller := slackrepopoller.New(token, channelID, slackrepopoller.Config{})
	return notifier, poller, nil
}
