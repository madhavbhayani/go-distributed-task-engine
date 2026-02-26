package executor

import (
	"fmt"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/analyzer"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// reanalyzeDataset runs the column analyzer on a dataset's current records
// and returns the fresh DatasetAnalysis. It also populates the ColumnStats
// map on the given ETLJobResult and updates the Dataset.Analysis field in
// the store so subsequent steps see the latest quality score.
func (e *Engine) reanalyzeDataset(ds *dataset.Dataset, result *models.ETLJobResult) {
	if ds == nil || result == nil || len(ds.Records) == 0 {
		return
	}

	// Convert dataset.Record (map[string]string) → [][]string rows for the analyzer.
	headers := ds.Columns
	totalRows := len(ds.Records)

	rows := make([][]string, totalRows)
	for i := 0; i < totalRows; i++ {
		row := make([]string, len(headers))
		for j, h := range headers {
			row[j] = ds.Records[i][h]
		}
		rows[i] = row
	}

	analysis := analyzer.Analyze(headers, rows)

	// Attach full analysis to the result.
	result.Analysis = analysis

	// Also build the ColumnStats map for backward compatibility.
	colStats := make(map[string]models.ColStat, len(analysis.Columns))
	for name, ca := range analysis.Columns {
		cs := models.ColStat{
			NullCount:   int64(ca.NullCount + ca.EmptyCount),
			UniqueCount: int64(ca.UniqueCount),
		}
		if ca.Min != nil {
			cs.MinValue = formatFloat(*ca.Min)
		}
		if ca.Max != nil {
			cs.MaxValue = formatFloat(*ca.Max)
		}
		colStats[name] = cs
	}
	result.ColumnStats = colStats

	// Update the stored dataset's analysis so later steps see fresh quality.
	ds.Analysis = analysis
	e.datasetStore.Put(ds)
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%.4f", f)
}
