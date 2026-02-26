package handlers

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
	"github.com/go-chi/chi/v5"
)

// ════════════════════════════════════════════════════════════════
//  Worker Pool & Dataset Handlers
// ════════════════════════════════════════════════════════════════

// WorkerHandler manages worker pool operations.
type WorkerHandler struct {
	pool         *worker.Pool
	datasetStore *dataset.Store
}

// NewWorkerHandler creates a new WorkerHandler.
func NewWorkerHandler(pool *worker.Pool, ds *dataset.Store) *WorkerHandler {
	return &WorkerHandler{pool: pool, datasetStore: ds}
}

// ──────────────────────────────────────────────
// GET /api/v1/workers
// ──────────────────────────────────────────────

func (h *WorkerHandler) GetPoolStats(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	respondJSON(w, http.StatusOK, success(reqID, "worker pool stats", h.pool.Stats()))
}

// ──────────────────────────────────────────────
// POST /api/v1/workers/scale
// Body: { "workers": 8 }
// ──────────────────────────────────────────────

type scaleRequest struct {
	Workers int `json:"workers"`
}

func (h *WorkerHandler) ScalePool(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req scaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body", nil))
		return
	}

	newCount, err := h.pool.Scale(req.Workers)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "SCALE_FAILED", err.Error(), nil))
		return
	}

	respondJSON(w, http.StatusOK, success(reqID, "worker pool scaled", map[string]any{
		"workers": newCount,
		"stats":   h.pool.Stats(),
	}))
}

// ──────────────────────────────────────────────
// GET /api/v1/datasets
// ──────────────────────────────────────────────

func (h *WorkerHandler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	respondJSON(w, http.StatusOK, success(reqID, "datasets listed", h.datasetStore.List()))
}

// ──────────────────────────────────────────────
// GET /api/v1/datasets/{datasetID}
// ──────────────────────────────────────────────

func (h *WorkerHandler) GetDataset(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	dsID := chi.URLParam(r, "datasetID")

	ds, err := h.datasetStore.Get(dsID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "DATASET_NOT_FOUND", err.Error(), nil))
		return
	}

	// Return summary + first 100 records (avoid huge payloads)
	records := ds.Records
	truncated := false
	if len(records) > 100 {
		records = records[:100]
		truncated = true
	}

	respondJSON(w, http.StatusOK, success(reqID, "dataset retrieved", map[string]any{
		"id":           ds.ID,
		"columns":      ds.Columns,
		"record_count": len(ds.Records),
		"records":      records,
		"truncated":    truncated,
	}))
}

// ──────────────────────────────────────────────
// GET /api/v1/datasets/{datasetID}/export
// Returns the full dataset as a CSV file download.
// ──────────────────────────────────────────────

func (h *WorkerHandler) ExportDataset(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	dsID := chi.URLParam(r, "datasetID")

	ds, err := h.datasetStore.Get(dsID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "DATASET_NOT_FOUND", err.Error(), nil))
		return
	}

	filename := fmt.Sprintf("%s_export.csv", strings.ReplaceAll(dsID, "/", "_"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	cw := csv.NewWriter(w)
	// Write header row
	if err := cw.Write(ds.Columns); err != nil {
		return
	}
	// Write every record
	for _, rec := range ds.Records {
		row := make([]string, len(ds.Columns))
		for i, col := range ds.Columns {
			row[i] = rec[col]
		}
		if err := cw.Write(row); err != nil {
			return
		}
	}
	cw.Flush()
}

// ──────────────────────────────────────────────
// GET /api/v1/datasets/{datasetID}/analysis
// Returns the column-analysis report produced at import time.
// ──────────────────────────────────────────────

func (h *WorkerHandler) GetDatasetAnalysis(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	dsID := chi.URLParam(r, "datasetID")

	ds, err := h.datasetStore.Get(dsID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "DATASET_NOT_FOUND", err.Error(), nil))
		return
	}
	if ds.Analysis == nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "ANALYSIS_NOT_FOUND", "No analysis available for this dataset", nil))
		return
	}
	respondJSON(w, http.StatusOK, success(reqID, "dataset analysis", ds.Analysis))
}

// ──────────────────────────────────────────────
// GET /api/v1/datasets/export-zip?ids=id1,id2,id3
// Returns multiple datasets as CSV files packed in a ZIP.
// ──────────────────────────────────────────────

func (h *WorkerHandler) ExportDatasetsZip(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "MISSING_IDS", "Query parameter 'ids' is required", nil))
		return
	}

	ids := strings.Split(idsParam, ",")
	if len(ids) == 0 {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "EMPTY_IDS", "At least one dataset ID is required", nil))
		return
	}

	// Collect all datasets first to validate
	datasets := make([]*dataset.Dataset, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ds, err := h.datasetStore.Get(id)
		if err != nil {
			respondJSON(w, http.StatusNotFound, apiError(reqID, "DATASET_NOT_FOUND", fmt.Sprintf("dataset not found: %s", id), nil))
			return
		}
		datasets = append(datasets, ds)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="normalized_tables.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, ds := range datasets {
		filename := fmt.Sprintf("%s.csv", strings.ReplaceAll(ds.ID, "/", "_"))
		fw, err := zw.Create(filename)
		if err != nil {
			return
		}

		cw := csv.NewWriter(fw)
		if err := cw.Write(ds.Columns); err != nil {
			return
		}
		for _, rec := range ds.Records {
			row := make([]string, len(ds.Columns))
			for i, col := range ds.Columns {
				row[i] = rec[col]
			}
			if err := cw.Write(row); err != nil {
				return
			}
		}
		cw.Flush()
	}
}
