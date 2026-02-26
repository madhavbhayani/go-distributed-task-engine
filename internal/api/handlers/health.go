package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dispatcher"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/monitor"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/worker"
)

// ════════════════════════════════════════════════════════════════
//  Health Check Handler
// ════════════════════════════════════════════════════════════════

// HealthHandler serves system health status.
type HealthHandler struct {
	version    string
	startTime  time.Time
	dispatcher *dispatcher.Dispatcher
	ramMonitor *monitor.RAMMonitor
	workerPool *worker.Pool
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(version string, d *dispatcher.Dispatcher, ram *monitor.RAMMonitor, wp *worker.Pool) *HealthHandler {
	return &HealthHandler{
		version:    version,
		startTime:  time.Now(),
		dispatcher: d,
		ramMonitor: ram,
		workerPool: wp,
	}
}

// HealthCheck handles GET /api/v1/health
func (h *HealthHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	ramStats := h.ramMonitor.Stats()
	overallStatus := "healthy"
	if ramStats.UsagePct > 90 {
		overallStatus = "degraded"
	}

	health := models.HealthResponse{
		Status:    overallStatus,
		Version:   h.version,
		Uptime:    time.Since(h.startTime).Round(time.Second).String(),
		Timestamp: time.Now().UTC(),
		Checks: map[string]models.HealthCheck{
			"job_store": {
				Status:  "up",
				Message: "in-memory store operational",
			},
			"dispatcher": {
				Status:  "up",
				Message: fmt.Sprintf("dispatched=%d rejected=%d", h.dispatcher.Stats().TotalDispatched, h.dispatcher.Stats().TotalRejected),
			},
			"priority_queue": {
				Status:  "up",
				Message: fmt.Sprintf("total=%d high=%d med=%d low=%d", h.dispatcher.Stats().QueueStats.TotalCount, h.dispatcher.Stats().QueueStats.HighCount, h.dispatcher.Stats().QueueStats.MediumCount, h.dispatcher.Stats().QueueStats.LowCount),
			},
			"ram": {
				Status:  boolToStatus(ramStats.UnderCap),
				Message: fmt.Sprintf("%.1f MB / %.0f MB (%.1f%%)", ramStats.AllocMB, ramStats.CapMB, ramStats.UsagePct),
			},
			"worker_pool": {
				Status:  "up",
				Message: fmt.Sprintf("workers=%d in_flight=%d processed=%d", h.workerPool.Stats().ActiveWorkers, h.workerPool.Stats().InFlight, h.workerPool.Stats().JobsProcessed),
			},
		},
	}

	respondJSON(w, http.StatusOK, success(reqID, "system "+overallStatus, health))
}

// SystemStats handles GET /api/v1/stats
func (h *HealthHandler) SystemStats(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	ds := h.dispatcher.Stats()
	rs := h.ramMonitor.Stats()

	data := map[string]any{
		"dispatcher":  ds,
		"ram":         rs,
		"worker_pool": h.workerPool.Stats(),
		"uptime":      time.Since(h.startTime).Round(time.Second).String(),
	}

	respondJSON(w, http.StatusOK, success(reqID, "system stats", data))
}

// ReadinessCheck handles GET /api/v1/ready
func (h *HealthHandler) ReadinessCheck(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	ramStats := h.ramMonitor.Stats()
	if !ramStats.UnderCap {
		respondJSON(w, http.StatusServiceUnavailable,
			apiError(reqID, "RAM_EXCEEDED", "RAM cap exceeded — not ready to accept jobs", nil))
		return
	}
	respondJSON(w, http.StatusOK, success(reqID, "ready", map[string]string{"status": "ready"}))
}

// LivenessCheck handles GET /api/v1/live
func (h *HealthHandler) LivenessCheck(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)
	respondJSON(w, http.StatusOK, success(reqID, "alive", map[string]string{"status": "alive"}))
}

func boolToStatus(b bool) string {
	if b {
		return "up"
	}
	return "down"
}
