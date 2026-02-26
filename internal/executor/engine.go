package executor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
)

// ════════════════════════════════════════════════════════════════
//  Job Executor Engine
//
//  Central dispatch layer that routes each job type to the
//  correct executor implementation.
//
//  Currently supports ETL executors:
//    • data_import     — CSV streaming import with chunk processing
//    • data_clean      — rule-based data cleaning
//    • data_normalize  — column normalization
//    • data_deduplicate — duplicate detection & removal
// ════════════════════════════════════════════════════════════════

// Engine is the top-level executor that dispatches to type-specific handlers.
type Engine struct {
	jobStore     store.JobStore
	datasetStore *dataset.Store
	logger       *slog.Logger
}

// New creates a new executor engine.
func New(js store.JobStore, ds *dataset.Store, logger *slog.Logger) *Engine {
	return &Engine{
		jobStore:     js,
		datasetStore: ds,
		logger:       logger,
	}
}

// DatasetStore returns the underlying dataset store (for API access).
func (e *Engine) DatasetStore() *dataset.Store {
	return e.datasetStore
}

// Execute runs a job and returns its result. Called by the worker pool.
// The context carries the per-job timeout.
func (e *Engine) Execute(ctx context.Context, job *models.Job) (any, error) {
	switch job.Type {
	// ── ETL jobs ──────────────────────────────
	case models.JobTypeETLImport:
		return e.executeETLImport(ctx, job)
	case models.JobTypeETLClean:
		return e.executeETLClean(ctx, job)
	case models.JobTypeETLNormalize:
		return e.executeETLNormalize(ctx, job)
	case models.JobTypeETLDeduplicate:
		return e.executeETLDeduplicate(ctx, job)

	default:
		return nil, fmt.Errorf("unsupported job type: %s", job.Type)
	}
}
