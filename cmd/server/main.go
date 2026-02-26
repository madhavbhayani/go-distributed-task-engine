package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/api"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/config"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dispatcher"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/executor"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/monitor"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/queue"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
)

// Build-time variables (set via -ldflags)
var (
	version   = "0.1.0"
	buildTime = "unknown"
)

func main() {
	// ── Configuration ─────────────────────────────
	cfg := config.Load()

	// ── Logger ────────────────────────────────────
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)}
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// ── Startup banner ────────────────────────────
	logger.Info("═══════════════════════════════════════════════════")
	logger.Info("  GO DISTRIBUTED JOB PROCESSING ENGINE")
	logger.Info("═══════════════════════════════════════════════════",
		slog.String("version", version),
		slog.String("build_time", buildTime),
		slog.Int("port", cfg.Server.Port),
		slog.Int("max_workers", cfg.Engine.MaxWorkers),
		slog.Int("queue_size", cfg.Engine.QueueSize),
		slog.Int64("ram_cap_mb", cfg.Engine.RAMCapMB),
		slog.Int64("queue_ram_cap_mb", cfg.Engine.QueueRAMCapMB),
		slog.Bool("auth_enabled", cfg.Auth.Enabled),
		slog.Bool("rate_limit_enabled", cfg.RateLimit.Enabled),
	)

	// ── Job Store ─────────────────────────────────
	jobStore := store.NewMemoryStore()
	logger.Info("Job store initialized", slog.String("type", "in-memory"))

	// ── Priority Queue ────────────────────────────
	pq := queue.NewPriorityQueue(cfg.Engine.QueueRAMCapMB, logger)
	logger.Info("Priority queue initialized",
		slog.Int64("ram_cap_mb", cfg.Engine.QueueRAMCapMB),
		slog.String("tiers", "HIGH/MEDIUM/LOW"),
	)

	// ── Job Dispatcher ────────────────────────────
	dispatchCfg := dispatcher.DefaultConfig()
	jobDispatcher := dispatcher.New(jobStore, pq, dispatchCfg, logger)
	logger.Info("Dispatcher initialized",
		slog.Duration("poll_interval", dispatchCfg.PollInterval),
		slog.Int("max_batch_size", dispatchCfg.MaxBatchSize),
	)

	// ── RAM Monitor ───────────────────────────────
	ramMon := monitor.NewRAMMonitor(cfg.Engine.RAMCapMB, logger)
	logger.Info("RAM monitor initialized", slog.Int64("cap_mb", cfg.Engine.RAMCapMB))

	// ── Dataset Store ─────────────────────────────
	datasetStore := dataset.NewStore()
	logger.Info("Dataset store initialized", slog.String("type", "in-memory"))

	// ── Executor Engine ──────────────────────────
	exec := executor.New(jobStore, datasetStore, logger)
	logger.Info("Executor engine initialized")

	// ── Worker Pool ──────────────────────────────
	workerPool := worker.New(jobStore, pq, exec, worker.DefaultPoolConfig(), logger)
	logger.Info("Worker pool initialized",
		slog.Int("min_workers", worker.MinWorkers),
		slog.Int("max_workers", worker.MaxWorkers),
	)

	// ── Start background systems ──────────────────
	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	jobDispatcher.Start(ctx)
	ramMon.Start(2 * time.Second)
	workerPool.Start(ctx)
	logger.Info("Background systems started (dispatcher, RAM monitor, worker pool)")

	// ── HTTP Router ───────────────────────────────
	router := api.NewRouter(api.RouterDeps{
		Store:        jobStore,
		Dispatcher:   jobDispatcher,
		RAMMonitor:   ramMon,
		WorkerPool:   workerPool,
		DatasetStore: datasetStore,
		Config:       cfg,
		Logger:       logger,
		Version:      version,
	})

	// ── HTTP Server ───────────────────────────────
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// ── Graceful Shutdown ─────────────────────────
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("HTTP server starting", slog.String("addr", addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	// ── Self-Ping Keepalive (Render anti-sleep) ───
	go func() {
		const selfURL = "https://dataforge-etl-pipeline-engine.onrender.com/api/v1/health"
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		// Initial ping after a short delay to let server start.
		time.Sleep(5 * time.Second)
		for {
			resp, err := http.Get(selfURL)
			if err != nil {
				logger.Warn("Keepalive ping failed", slog.String("error", err.Error()))
			} else {
				resp.Body.Close()
				logger.Debug("Keepalive ping OK", slog.Int("status", resp.StatusCode))
			}
			select {
			case <-ticker.C:
			case <-done:
				return
			}
		}
	}()

	logger.Info("Server is ready to accept connections")
	logger.Info("API docs: http://" + addr + "/api/v1/health")

	// Wait for shutdown signal
	<-done
	logger.Info("Shutdown signal received, draining connections...")

	// Stop background systems first
	workerPool.Stop()
	jobDispatcher.Stop()
	ramMon.Stop()
	logger.Info("Background systems stopped")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server forced shutdown", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("Server stopped gracefully")
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// init ensures the data directory exists.
func init() {
	cfg := config.Load()
	if err := os.MkdirAll(cfg.Engine.DataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create data directory: %v\n", err)
	}
	_ = time.Now() // reference time package
}
