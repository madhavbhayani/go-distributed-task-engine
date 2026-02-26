package handlers

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/analyzer"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
)

// ════════════════════════════════════════════════════════════════
//  CSV Upload Handler
//
//  POST /api/v1/upload/csv
//  Accepts a multipart/form-data field named "file" containing a
//  CSV file (max 10 MB).  Reads the whole file into RAM, runs the
//  column analyzer, stores the resulting Dataset and returns an
//  upload summary so the frontend can display a preview immediately.
// ════════════════════════════════════════════════════════════════

const maxUploadBytes = 10 << 20 // 10 MB

// UploadHandler handles direct CSV file uploads.
type UploadHandler struct {
	datasetStore *dataset.Store
}

// NewUploadHandler creates a new UploadHandler.
func NewUploadHandler(ds *dataset.Store) *UploadHandler {
	return &UploadHandler{datasetStore: ds}
}

// UploadResult is the JSON response body for a successful upload.
type UploadResult struct {
	DatasetID string                    `json:"dataset_id"`
	Filename  string                    `json:"filename"`
	Rows      int                       `json:"rows"`
	Columns   []string                  `json:"columns"`
	Analysis  *analyzer.DatasetAnalysis `json:"analysis"`
}

// ──────────────────────────────────────────────
// POST /api/v1/upload/csv
// ──────────────────────────────────────────────

func (h *UploadHandler) UploadCSV(w http.ResponseWriter, r *http.Request) {
	reqID := getRequestID(r)

	// ── Parse multipart (memory limit = maxUploadBytes) ──
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"PARSE_ERROR", "Could not parse multipart form: "+err.Error(), nil))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"MISSING_FILE", "Field 'file' is required in the multipart body", nil))
		return
	}
	defer file.Close()

	// ── Validate extension ───────────────────────
	name := header.Filename
	if ext := strings.ToLower(filepath.Ext(name)); ext != ".csv" {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"INVALID_FORMAT", fmt.Sprintf("Only .csv files are accepted, got %q", ext), nil))
		return
	}

	// ── Read entire file into memory ─────────────
	raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, apiError(reqID,
			"READ_ERROR", "Failed to read uploaded file: "+err.Error(), nil))
		return
	}
	if int64(len(raw)) > maxUploadBytes {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"FILE_TOO_LARGE", "File exceeds the 10 MB limit", nil))
		return
	}

	// ── Parse CSV ────────────────────────────────
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	allRows, err := reader.ReadAll()
	if err != nil {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"CSV_PARSE_ERROR", "Failed to parse CSV: "+err.Error(), nil))
		return
	}
	if len(allRows) < 2 {
		respondJSON(w, http.StatusBadRequest, apiError(reqID,
			"EMPTY_CSV", "CSV must have at least a header row and one data row", nil))
		return
	}

	headers := allRows[0]
	dataRows := allRows[1:]

	// ── Build dataset records ─────────────────────
	records := make([]dataset.Record, 0, len(dataRows))
	for _, row := range dataRows {
		rec := make(dataset.Record, len(headers))
		for i, h := range headers {
			if i < len(row) {
				rec[h] = row[i]
			} else {
				rec[h] = ""
			}
		}
		records = append(records, rec)
	}

	// ── Run column analyzer on ALL rows ──
	analysis := analyzer.Analyze(headers, dataRows)

	// ── Store dataset in memory ───────────────────
	dsID := uuid.New().String()
	ds := &dataset.Dataset{
		ID:       dsID,
		Columns:  headers,
		Records:  records,
		Analysis: analysis,
	}
	h.datasetStore.Put(ds)

	respondJSON(w, http.StatusOK, success(reqID, "CSV uploaded and loaded into memory", UploadResult{
		DatasetID: dsID,
		Filename:  name,
		Rows:      len(records),
		Columns:   headers,
		Analysis:  analysis,
	}))
}
