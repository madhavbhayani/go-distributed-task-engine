package handlers

import (
	"math"
	"net/http"
	"strconv"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
	"github.com/go-chi/chi/v5"
)

// ════════════════════════════════════════════════════════════════
//  Job Management Handlers (CRUD + status operations)
// ════════════════════════════════════════════════════════════════

// JobHandler manages generic job CRUD operations.
type JobHandler struct {
	store store.JobStore
}

// NewJobHandler creates a new JobHandler.
func NewJobHandler(s store.JobStore) *JobHandler {
	return &JobHandler{store: s}
}

// GetJob handles GET /api/v1/jobs/{jobID}
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	jobID := chi.URLParam(r, "jobID")

	job, err := h.store.Get(jobID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "JOB_NOT_FOUND", "Job not found", map[string]string{"job_id": jobID}))
		return
	}

	respondJSON(w, http.StatusOK, success(reqID, "job retrieved", job))
}

// ListJobs handles GET /api/v1/jobs
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	query := r.URL.Query()

	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	priority, _ := strconv.Atoi(query.Get("priority"))

	filter := store.ListFilter{
		Status:   models.JobStatus(query.Get("status")),
		Type:     models.JobType(query.Get("type")),
		Priority: models.Priority(priority),
		Tag:      query.Get("tag"),
		Page:     page,
		PageSize: pageSize,
		SortBy:   query.Get("sort_by"),
		SortDir:  query.Get("sort_dir"),
	}

	jobs, total, err := h.store.List(filter)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID, "LIST_FAILED", err.Error(), nil))
		return
	}

	if pageSize < 1 {
		pageSize = 20
	}

	paginated := models.PaginatedResponse{
		Items:      jobs,
		TotalCount: total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: int(math.Ceil(float64(total) / float64(pageSize))),
	}

	respondJSON(w, http.StatusOK, success(reqID, "jobs listed", paginated))
}

// CancelJob handles DELETE /api/v1/jobs/{jobID}
func (h *JobHandler) CancelJob(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	jobID := chi.URLParam(r, "jobID")

	job, err := h.store.Get(jobID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "JOB_NOT_FOUND", "Job not found", map[string]string{"job_id": jobID}))
		return
	}

	// Only allow cancellation of pending/queued/running jobs
	switch job.Status {
	case models.StatusPending, models.StatusQueued, models.StatusRunning:
		if err := h.store.UpdateStatus(jobID, models.StatusCancelled); err != nil {
			respondJSON(w, http.StatusInternalServerError, apiError(reqID, "CANCEL_FAILED", err.Error(), nil))
			return
		}
		respondJSON(w, http.StatusOK, success(reqID, "job cancelled", map[string]string{"job_id": jobID, "status": string(models.StatusCancelled)}))
	default:
		respondJSON(w, http.StatusConflict, apiError(reqID, "INVALID_STATE",
			"Cannot cancel a job with status: "+string(job.Status),
			map[string]string{"current_status": string(job.Status)},
		))
	}
}

// GetJobProgress handles GET /api/v1/jobs/{jobID}/progress
func (h *JobHandler) GetJobProgress(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	jobID := chi.URLParam(r, "jobID")

	job, err := h.store.Get(jobID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, apiError(reqID, "JOB_NOT_FOUND", "Job not found", map[string]string{"job_id": jobID}))
		return
	}

	data := map[string]any{
		"job_id":   job.ID,
		"type":     job.Type,
		"status":   job.Status,
		"progress": job.Progress,
	}

	if d := job.Duration(); d != nil {
		data["elapsed"] = d.String()
	}

	respondJSON(w, http.StatusOK, success(reqID, "progress retrieved", data))
}
