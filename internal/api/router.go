package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/api/handlers"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/api/middleware"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/config"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dispatcher"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/monitor"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
)

// ════════════════════════════════════════════════════════════════
//  Router Setup
//  Defines all API routes and wires up handlers + gateway.
// ════════════════════════════════════════════════════════════════

// RouterDeps bundles all dependencies for constructing the router.
type RouterDeps struct {
	Store        store.JobStore
	Dispatcher   *dispatcher.Dispatcher
	RAMMonitor   *monitor.RAMMonitor
	WorkerPool   *worker.Pool
	DatasetStore *dataset.Store
	Config       *config.Config
	Logger       *slog.Logger
	Version      string
}

// NewRouter creates and configures the HTTP router with all routes.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	// ── Global middleware (always active) ──────────
	r.Use(middleware.Recovery(deps.Logger))
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(deps.Logger))
	r.Use(middleware.CORS)

	// Auth removed — public access for all users

	// ── API Gateway: Rate Limiting (production only)
	r.Use(middleware.RateLimit(middleware.RateLimitConfig{
		Enabled:         deps.Config.RateLimit.Enabled,
		RequestsPerMin:  deps.Config.RateLimit.RequestsPerMin,
		BurstSize:       deps.Config.RateLimit.BurstSize,
		CleanupInterval: 5 * time.Minute,
	}))

	// ── Handlers ──────────────────────────────────
	healthHandler := handlers.NewHealthHandler(deps.Version, deps.Dispatcher, deps.RAMMonitor, deps.WorkerPool)
	jobHandler := handlers.NewJobHandler(deps.Store)
	etlHandler := handlers.NewETLHandler(deps.Store, deps.Dispatcher)
	workerHandler := handlers.NewWorkerHandler(deps.WorkerPool, deps.DatasetStore)
	uploadHandler := handlers.NewUploadHandler(deps.DatasetStore)

	// ── API v1 Routes ─────────────────────────────
	r.Route("/api/v1", func(r chi.Router) {

		// Health / Probes (no auth, no rate limit)
		r.Get("/health", healthHandler.HealthCheck)
		r.Get("/ready", healthHandler.ReadinessCheck)
		r.Get("/live", healthHandler.LivenessCheck)

		// ── System Stats ──────────────────────────
		r.Get("/stats", healthHandler.SystemStats) // dispatcher + queue + RAM stats

		// ── Worker Pool Management ─────────────────
		r.Get("/workers", workerHandler.GetPoolStats)
		r.Post("/workers/scale", workerHandler.ScalePool)

		// ── CSV File Upload ─────────────────────────
		r.Post("/upload/csv", uploadHandler.UploadCSV)

		// ── Dataset Management ─────────────────────
		r.Get("/datasets", workerHandler.ListDatasets)
		r.Get("/datasets/export-zip", workerHandler.ExportDatasetsZip)
		r.Get("/datasets/{datasetID}", workerHandler.GetDataset)
		r.Get("/datasets/{datasetID}/export", workerHandler.ExportDataset)
		r.Get("/datasets/{datasetID}/analysis", workerHandler.GetDatasetAnalysis)

		// ── Job Management ────────────────────────
		r.Route("/jobs", func(r chi.Router) {
			r.Get("/", jobHandler.ListJobs)
			r.Get("/{jobID}", jobHandler.GetJob)
			r.Delete("/{jobID}", jobHandler.CancelJob)
			r.Get("/{jobID}/progress", jobHandler.GetJobProgress)

			// ── ETL / Data Migration ──────────────
			r.Route("/etl", func(r chi.Router) {
				r.Post("/import", etlHandler.ImportCSV)
				r.Post("/clean", etlHandler.CleanData)
				r.Post("/normalize", etlHandler.NormalizeData)
				r.Post("/deduplicate", etlHandler.DeduplicateData)
				r.Post("/pipeline", etlHandler.Pipeline)
			})
		})
	})

	// ── Root fallback ─────────────────────────────
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"error":{"code":"NOT_FOUND","message":"Endpoint not found"}}`))
	})

	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"success":false,"error":{"code":"METHOD_NOT_ALLOWED","message":"Method not allowed"}}`))
	})

	return r
}
