package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dispatcher"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/validator"
)

// ════════════════════════════════════════════════════════════════
//  ETL / Data Migration Handlers
//  Endpoints for submitting ETL and data processing jobs.
// ════════════════════════════════════════════════════════════════

// ETLHandler handles ETL job submissions.
type ETLHandler struct {
	store      store.JobStore
	dispatcher *dispatcher.Dispatcher
}

// NewETLHandler creates a new ETLHandler.
func NewETLHandler(s store.JobStore, d *dispatcher.Dispatcher) *ETLHandler {
	return &ETLHandler{store: s, dispatcher: d}
}

// ──────────────────────────────────────────────
// POST /api/v1/jobs/etl/import
// ──────────────────────────────────────────────

func (h *ETLHandler) ImportCSV(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req models.CreateJobRequest
	if err := decodeJSON(r, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateCreateJobRequest(&req); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid request", ve.Fields))
			return
		}
	}

	var params models.ETLImportParams
	paramsBytes, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_PARAMS", "Invalid import params: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateETLImportParams(&params); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid import params", ve.Fields))
			return
		}
	}

	// Set defaults
	if params.Delimiter == "" {
		params.Delimiter = ","
	}
	if params.Encoding == "" {
		params.Encoding = "utf-8"
	}
	if params.BatchSize == 0 {
		params.BatchSize = 5000
	}

	job := buildJob(req, models.JobTypeETLImport, params)
	if err := h.store.Create(job); err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CREATE_FAILED", err.Error(), nil))
		return
	}

	if err := h.dispatcher.Dispatch(job); err != nil {
		slog.Warn("dispatch failed, job stays pending", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}

	respondJSON(w, http.StatusAccepted, success(reqID, "CSV import job created", buildCreateResponse(job)))
}

// ──────────────────────────────────────────────
// POST /api/v1/jobs/etl/clean
// ──────────────────────────────────────────────

func (h *ETLHandler) CleanData(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req models.CreateJobRequest
	if err := decodeJSON(r, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateCreateJobRequest(&req); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid request", ve.Fields))
			return
		}
	}

	var params models.ETLCleanParams
	paramsBytes, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_PARAMS", "Invalid clean params: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateETLCleanParams(&params); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid clean params", ve.Fields))
			return
		}
	}

	if params.NullHandling == "" {
		params.NullHandling = "fill_default"
	}

	job := buildJob(req, models.JobTypeETLClean, params)
	if err := h.store.Create(job); err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CREATE_FAILED", err.Error(), nil))
		return
	}

	if err := h.dispatcher.Dispatch(job); err != nil {
		slog.Warn("dispatch failed, job stays pending", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}

	respondJSON(w, http.StatusAccepted, success(reqID, "Data cleaning job created", buildCreateResponse(job)))
}

// ──────────────────────────────────────────────
// POST /api/v1/jobs/etl/normalize
// ──────────────────────────────────────────────

func (h *ETLHandler) NormalizeData(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req models.CreateJobRequest
	if err := decodeJSON(r, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateCreateJobRequest(&req); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid request", ve.Fields))
			return
		}
	}

	var params models.ETLNormalizeParams
	paramsBytes, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_PARAMS", "Invalid normalize params: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateETLNormalizeParams(&params); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid normalize params", ve.Fields))
			return
		}
	}

	job := buildJob(req, models.JobTypeETLNormalize, params)
	if err := h.store.Create(job); err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CREATE_FAILED", err.Error(), nil))
		return
	}

	if err := h.dispatcher.Dispatch(job); err != nil {
		slog.Warn("dispatch failed, job stays pending", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}

	respondJSON(w, http.StatusAccepted, success(reqID, "Data normalization job created", buildCreateResponse(job)))
}

// ──────────────────────────────────────────────
// POST /api/v1/jobs/etl/deduplicate
// ──────────────────────────────────────────────

func (h *ETLHandler) DeduplicateData(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req models.CreateJobRequest
	if err := decodeJSON(r, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateCreateJobRequest(&req); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid request", ve.Fields))
			return
		}
	}

	var params models.ETLDeduplicateParams
	paramsBytes, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_PARAMS", "Invalid deduplicate params: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateETLDeduplicateParams(&params); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid deduplicate params", ve.Fields))
			return
		}
	}

	job := buildJob(req, models.JobTypeETLDeduplicate, params)
	if err := h.store.Create(job); err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CREATE_FAILED", err.Error(), nil))
		return
	}

	if err := h.dispatcher.Dispatch(job); err != nil {
		slog.Warn("dispatch failed, job stays pending", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}

	respondJSON(w, http.StatusAccepted, success(reqID, "Deduplication job created", buildCreateResponse(job)))
}

// ──────────────────────────────────────────────
// POST /api/v1/jobs/etl/pipeline
// ──────────────────────────────────────────────

func (h *ETLHandler) Pipeline(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	var req models.CreateJobRequest
	if err := decodeJSON(r, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_JSON", "Invalid request body: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateCreateJobRequest(&req); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid request", ve.Fields))
			return
		}
	}

	var params models.ETLPipelineParams
	paramsBytes, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID, "INVALID_PARAMS", "Invalid pipeline params: "+err.Error(), nil))
		return
	}

	if err := validator.ValidateETLPipelineParams(&params); err != nil {
		if ve, ok := err.(*validator.ValidationError); ok {
			respondJSON(w, http.StatusBadRequest, apiError(reqID, "VALIDATION_ERROR", "Invalid pipeline params", ve.Fields))
			return
		}
	}

	job := buildJob(req, models.JobTypeETLPipeline, params)
	if err := h.store.Create(job); err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CREATE_FAILED", err.Error(), nil))
		return
	}

	if err := h.dispatcher.Dispatch(job); err != nil {
		slog.Warn("dispatch failed, job stays pending", slog.String("job_id", job.ID), slog.String("error", err.Error()))
	}

	respondJSON(w, http.StatusAccepted, success(reqID, "ETL pipeline job created", buildCreateResponse(job)))
}
