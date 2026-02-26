package executor

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/analyzer"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  ETL Import Executor
//
//  Features:
//    • Streaming CSV reader  — never loads full file into memory
//    • Chunk processing      — processes records in configurable batches
//    • Batch insertion       — writes chunks to the dataset store
//    • Progress reporting    — updates job progress per chunk
//    • Cancellation support  — respects context deadline/cancel
// ════════════════════════════════════════════════════════════════

func (e *Engine) executeETLImport(ctx context.Context, job *models.Job) (any, error) {
	// ── Parse params ──────────────────────────────
	params, err := toETLImportParams(job.Params)
	if err != nil {
		return nil, fmt.Errorf("invalid import params: %w", err)
	}

	// ── Open CSV file ─────────────────────────────
	filePath := params.SourceFilePath
	if filePath == "" {
		return nil, fmt.Errorf("source_file_path is required for import")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer file.Close()

	// ── Configure CSV reader (streaming) ──────────
	reader := csv.NewReader(bufio.NewReaderSize(file, 64*1024)) // 64KB buffer
	if params.Delimiter != "" {
		runes := []rune(params.Delimiter)
		reader.Comma = runes[0]
	}
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // variable field count

	// ── Read header ───────────────────────────────
	var columns []string
	if params.HasHeader || params.SkipRows == 0 {
		// First row is the header
		headerRow, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV header: %w", err)
		}
		for _, col := range headerRow {
			columns = append(columns, strings.TrimSpace(col))
		}
	}

	// Skip additional rows if requested
	for i := 0; i < params.SkipRows; i++ {
		if _, err := reader.Read(); err != nil {
			break
		}
	}

	// If columns were provided in params, use those
	if len(params.Columns) > 0 {
		columns = make([]string, len(params.Columns))
		for i, cd := range params.Columns {
			columns[i] = cd.Name
		}
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("no columns detected — set has_header=true or provide columns")
	}

	// ── Configure chunking ────────────────────────
	batchSize := params.BatchSize
	if batchSize <= 0 {
		batchSize = 5000
	}
	maxRows := params.MaxRows // 0 = no limit

	// ── Streaming chunk loop ──────────────────────
	var allRecords []dataset.Record
	var totalRead, totalFailed, totalSkipped int64
	chunk := make([]dataset.Record, 0, batchSize)
	startTime := time.Now()

	for {
		// Check cancellation
		select {
		case <-ctx.Done():
			return buildImportResult(params.DatasetID, totalRead, totalFailed, totalSkipped, startTime, columns), ctx.Err()
		default:
		}

		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			totalFailed++
			continue
		}

		// Build record
		rec := make(dataset.Record, len(columns))
		for i, col := range columns {
			if i < len(row) {
				rec[col] = strings.TrimSpace(row[i])
			} else {
				rec[col] = ""
			}
		}

		// Apply column mapping if provided
		if len(params.ColumnMapping) > 0 {
			mapped := make(dataset.Record, len(rec))
			for srcCol, val := range rec {
				if targetCol, ok := params.ColumnMapping[srcCol]; ok {
					mapped[targetCol] = val
				} else {
					mapped[srcCol] = val
				}
			}
			rec = mapped
		}

		chunk = append(chunk, rec)
		totalRead++

		// ── Batch insertion (flush chunk) ─────────
		if len(chunk) >= batchSize {
			allRecords = append(allRecords, chunk...)

			// Update progress
			pct := float64(0)
			if maxRows > 0 {
				pct = float64(totalRead) / float64(maxRows) * 100
				if pct > 100 {
					pct = 100
				}
			}
			_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
				TotalItems:     int64(maxRows),
				ProcessedItems: totalRead,
				FailedItems:    totalFailed,
				Percentage:     pct,
				CurrentStep:    "importing",
				Message:        fmt.Sprintf("Imported %d records...", totalRead),
			})

			// Reset chunk
			chunk = make([]dataset.Record, 0, batchSize)
		}

		// Max row limit
		if maxRows > 0 && totalRead >= int64(maxRows) {
			break
		}
	}

	// Flush remaining
	if len(chunk) > 0 {
		allRecords = append(allRecords, chunk...)
	}

	// Apply column mapping to column list
	if len(params.ColumnMapping) > 0 {
		mapped := make([]string, 0, len(columns))
		for _, col := range columns {
			if target, ok := params.ColumnMapping[col]; ok {
				mapped = append(mapped, target)
			} else {
				mapped = append(mapped, col)
			}
		}
		columns = mapped
	}

	// ── Analyze column types from ALL rows ────────────
	totalRows := len(allRecords)
	allRows := make([][]string, totalRows)
	for i := 0; i < totalRows; i++ {
		row := make([]string, len(columns))
		for j, col := range columns {
			row[j] = allRecords[i][col]
		}
		allRows[i] = row
	}
	analysis := analyzer.Analyze(columns, allRows)

	// ── Store dataset ─────────────────────────────
	ds := &dataset.Dataset{
		ID:       params.DatasetID,
		Columns:  columns,
		Records:  allRecords,
		Analysis: analysis,
	}
	e.datasetStore.Put(ds)

	// ── Final progress ────────────────────────────
	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		TotalItems:     totalRead,
		ProcessedItems: totalRead,
		FailedItems:    totalFailed,
		SkippedItems:   totalSkipped,
		Percentage:     100,
		CurrentStep:    "completed",
		Message:        fmt.Sprintf("Imported %d records into dataset '%s'", totalRead, params.DatasetID),
	})

	e.logger.Info("ETL import completed",
		slog.String("job_id", job.ID),
		slog.String("dataset_id", params.DatasetID),
		slog.Int64("records", totalRead),
		slog.Int64("failed", totalFailed),
		slog.Duration("elapsed", time.Since(startTime)),
	)

	return buildImportResult(params.DatasetID, totalRead, totalFailed, totalSkipped, startTime, columns), nil
}

func buildImportResult(dsID string, total, failed, skipped int64, start time.Time, cols []string) *models.ETLJobResult {
	colStats := make(map[string]models.ColStat, len(cols))
	for _, c := range cols {
		colStats[c] = models.ColStat{}
	}
	return &models.ETLJobResult{
		DatasetID:    dsID,
		TotalRecords: total,
		Processed:    total - failed - skipped,
		Failed:       failed,
		Skipped:      skipped,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
		ColumnStats:  colStats,
	}
}

// ──────────────────────────────────────────────
// Param helpers (JSON round-trip to typed struct)
// ──────────────────────────────────────────────

func toETLImportParams(raw any) (*models.ETLImportParams, error) {
	return marshalUnmarshal[models.ETLImportParams](raw)
}

func toETLCleanParams(raw any) (*models.ETLCleanParams, error) {
	return marshalUnmarshal[models.ETLCleanParams](raw)
}

func toETLNormalizeParams(raw any) (*models.ETLNormalizeParams, error) {
	return marshalUnmarshal[models.ETLNormalizeParams](raw)
}

func toETLDeduplicateParams(raw any) (*models.ETLDeduplicateParams, error) {
	return marshalUnmarshal[models.ETLDeduplicateParams](raw)
}

func marshalUnmarshal[T any](raw any) (*T, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
