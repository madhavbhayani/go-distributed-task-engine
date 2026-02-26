package models

import "github.com/madhavbhayani/go-distributed-task-engine/internal/analyzer"

// ════════════════════════════════════════════════════════════════
//  ETL / Data Migration Task Parameters
//  These structs define the JSON payload for each ETL job type.
// ════════════════════════════════════════════════════════════════

// ──────────────────────────────────────────────
// CSV Import
// ──────────────────────────────────────────────

// ETLImportParams defines parameters for importing CSV / flat-file data.
//
//	POST /api/v1/jobs/etl/import
type ETLImportParams struct {
	// Data source — provide one of: file path on server, or URL to download
	SourceFilePath string `json:"source_file_path,omitempty"`
	SourceURL      string `json:"source_url,omitempty"`

	// CSV parsing options
	Delimiter  string `json:"delimiter"    validate:"max=5"` // Default: ","
	QuoteChar  string `json:"quote_char"   validate:"max=1"` // Default: "\""
	EscapeChar string `json:"escape_char"  validate:"max=1"` // Default: "\\"
	HasHeader  bool   `json:"has_header"`                    // First row is header
	Encoding   string `json:"encoding"`                      // "utf-8" | "latin-1" | "utf-16"
	SkipRows   int    `json:"skip_rows"    validate:"min=0"` // Skip N rows from top
	MaxRows    int    `json:"max_rows"     validate:"min=0"` // 0 = no limit

	// Column definition (if no header or for override)
	Columns []ColumnDef `json:"columns,omitempty"`

	// Column mapping: source_col → target_col
	ColumnMapping map[string]string `json:"column_mapping,omitempty"`

	// Target dataset identifier (used for subsequent ETL steps)
	DatasetID string `json:"dataset_id" validate:"required"`

	// Processing tuning
	BatchSize   int `json:"batch_size"   validate:"min=1,max=100000"` // Default: 5000
	Concurrency int `json:"concurrency"  validate:"min=0,max=256"`
}

// ColumnDef defines a column's name and data type.
type ColumnDef struct {
	Name     string `json:"name"     validate:"required"`
	DataType string `json:"data_type" validate:"required,oneof=string int float bool date datetime"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
}

// ──────────────────────────────────────────────
// Data Cleaning
// ──────────────────────────────────────────────

// ETLCleanParams defines parameters for cleaning imported data.
//
//	POST /api/v1/jobs/etl/clean
type ETLCleanParams struct {
	// Reference to a previously imported dataset
	DatasetID string `json:"dataset_id" validate:"required"`

	// Cleaning rules applied in order
	Rules []CleaningRule `json:"rules" validate:"required,min=1"`

	// How to handle NULLs globally (can be overridden per rule)
	NullHandling string `json:"null_handling" validate:"oneof=drop fill_default fill_mean fill_median fill_custom skip"`

	// Per-column custom fill values (only used when NullHandling == "fill_custom")
	CustomFillValues map[string]string `json:"custom_fill_values,omitempty"`

	// If true, create a new dataset; if false, modify in-place
	CreateCopy bool   `json:"create_copy"`
	OutputID   string `json:"output_dataset_id,omitempty"`

	Concurrency int `json:"concurrency" validate:"min=0,max=256"`
}

// CleaningRule describes a single data-cleaning operation.
type CleaningRule struct {
	// Target column (empty = apply to all columns where applicable)
	Column string `json:"column,omitempty"`

	// Operation type
	// "trim_whitespace" | "to_lowercase" | "to_uppercase" | "remove_html"
	// "regex_replace" | "fill_null" | "drop_null" | "type_cast"
	// "remove_special_chars" | "standardize_date"
	Operation string `json:"operation" validate:"required"`

	// Operation-specific parameters
	Params map[string]string `json:"params,omitempty"`
}

// ──────────────────────────────────────────────
// Data Normalization (value-level + database 1NF/2NF/3NF)
// ──────────────────────────────────────────────

// NormalizationRule describes a single value-level normalization operation.
type NormalizationRule struct {
	// Target column
	Column string `json:"column" validate:"required"`

	// Operation type
	// "min_max_scale" | "z_score" | "email_normalize" | "phone_format"
	// "date_format" | "enum_map" | "url_normalize"
	// "to_lowercase" | "to_uppercase" | "trim"
	// "currency_format" | "unit_convert"
	Operation string `json:"operation" validate:"required"`

	// Operation-specific parameters
	Params map[string]string `json:"params,omitempty"`
}

// ETLNormalizeParams defines parameters for normalization.
// Supports two complementary modes:
//
//  1. Value-level transforms (Rules) — per-cell operations like scaling, formatting, etc.
//  2. Database normalization (NormalForm) — decompose flat table into related tables (1NF/2NF/3NF).
//
// Both modes can be combined: value-level transforms run first, then database decomposition.
//
//	POST /api/v1/jobs/etl/normalize
type ETLNormalizeParams struct {
	DatasetID string `json:"dataset_id" validate:"required"`

	// ── Value-level transforms ──
	// Optional array of per-column normalization rules.
	Rules []NormalizationRule `json:"rules,omitempty"`

	// ── Database normalization ──
	// Target normal form: 0 (skip), 1, 2, or 3. Default 0.
	NormalForm int `json:"normal_form,omitempty"`

	// Primary key column — required for 2NF and 3NF.
	PrimaryKeyColumn string `json:"primary_key_column,omitempty"`

	// Categorical columns to decompose into lookup tables (2NF/3NF).
	CategoricalColumns []string `json:"categorical_columns,omitempty"`

	CreateCopy bool   `json:"create_copy"`
	OutputID   string `json:"output_dataset_id,omitempty"`

	Concurrency int `json:"concurrency" validate:"min=0,max=256"`
}

// ──────────────────────────────────────────────
// Normalize Report — detailed decomposition log
// ──────────────────────────────────────────────

// NormalizeReport describes the result of a normalization pass (value-level + database).
type NormalizeReport struct {
	// ── Value-level transform summary ──
	ValueLevel *ValueLevelReport `json:"value_level,omitempty"`

	// ── Database normalization summary ──
	NormalForm  int    `json:"normal_form,omitempty"`
	PrimaryKey  string `json:"primary_key,omitempty"`
	Description string `json:"description,omitempty"`

	// Tables produced by decomposition (main + lookups)
	Tables []DecomposedTable `json:"tables,omitempty"`

	// Foreign-key relationships between the produced tables
	Relationships []TableRelationship `json:"relationships,omitempty"`

	// 1NF-specific: cells that were split into atomic values
	MultiValueSplits []MultiValueSplit `json:"multi_value_splits,omitempty"`
}

// ValueLevelReport describes the result of value-level normalization transforms.
type ValueLevelReport struct {
	TotalCellsModified int64           `json:"total_cells_modified"`
	Operations         []NormOpSummary `json:"operations"`
}

// NormOpSummary describes the effect of a single value-level normalization operation.
type NormOpSummary struct {
	Operation     string   `json:"operation"`
	Column        string   `json:"column"`
	CellsAffected int64    `json:"cells_affected"`
	Reason        string   `json:"reason,omitempty"`
	SampleBefore  []string `json:"sample_before,omitempty"` // up to 5 original values
	SampleAfter   []string `json:"sample_after,omitempty"`  // corresponding normalized values
}

// DecomposedTable describes a single table produced by normalization.
type DecomposedTable struct {
	DatasetID   string   `json:"dataset_id"`
	Name        string   `json:"name"`
	Columns     []string `json:"columns"`
	RecordCount int      `json:"record_count"`
	Description string   `json:"description"`
	IsMain      bool     `json:"is_main"`
}

// TableRelationship describes a foreign-key relationship between two tables.
type TableRelationship struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	Type       string `json:"type"` // "many-to-one"
}

// MultiValueSplit records a column where multi-valued cells were split for 1NF.
type MultiValueSplit struct {
	Column       string `json:"column"`
	CellsSplit   int    `json:"cells_split"`
	RowsBefore   int    `json:"rows_before"`
	RowsAfter    int    `json:"rows_after"`
	SampleBefore string `json:"sample_before,omitempty"`
	SampleAfter  string `json:"sample_after,omitempty"`
}

// ──────────────────────────────────────────────
// Deduplication
// ──────────────────────────────────────────────

// ETLDeduplicateParams defines parameters for detecting and removing duplicates.
//
//	POST /api/v1/jobs/etl/deduplicate
type ETLDeduplicateParams struct {
	DatasetID string `json:"dataset_id" validate:"required"`

	// Columns to match on for duplicate detection
	MatchColumns []string `json:"match_columns" validate:"required,min=1"`

	// Strategy: "exact" | "fuzzy"
	Strategy string `json:"strategy" validate:"required,oneof=exact fuzzy"`

	// Fuzzy matching threshold (0.0 – 1.0). Only used when strategy = "fuzzy"
	FuzzyThreshold float64 `json:"fuzzy_threshold" validate:"min=0,max=1"`

	// Which duplicate to keep: "first" | "last" | "most_complete"
	KeepStrategy string `json:"keep_strategy" validate:"required,oneof=first last most_complete"`

	// If true, only flag duplicates without removing them
	DryRun bool `json:"dry_run"`

	CreateCopy bool   `json:"create_copy"`
	OutputID   string `json:"output_dataset_id,omitempty"`

	Concurrency int `json:"concurrency" validate:"min=0,max=256"`
}

// ──────────────────────────────────────────────
// Full ETL Pipeline (chained steps)
// ──────────────────────────────────────────────

// ETLPipelineStepConfig holds the configuration for one step in an ETL pipeline.
type ETLPipelineStepConfig struct {
	// Step action: "import" | "clean" | "normalize" | "deduplicate"
	Action string `json:"action" validate:"required,oneof=import clean normalize deduplicate"`

	// Step-specific configuration
	Config any `json:"config" validate:"required"`
}

// ETLTarget describes where the final output should be written.
type ETLTarget struct {
	// Type: "file" | "database" | "api"
	Type string `json:"type" validate:"required,oneof=file database api"`

	// File output
	FilePath   string `json:"file_path,omitempty"`
	FileFormat string `json:"file_format,omitempty" validate:"omitempty,oneof=csv json parquet"`

	// Database output
	ConnectionString string `json:"connection_string,omitempty"`
	TableName        string `json:"table_name,omitempty"`
	UpsertKey        string `json:"upsert_key,omitempty"` // Column for upsert

	// API output
	WebhookURL string `json:"webhook_url,omitempty"`
}

// ETLPipelineParams defines a multi-step ETL pipeline.
//
//	POST /api/v1/jobs/etl/pipeline
type ETLPipelineParams struct {
	// Pipeline name
	Name string `json:"name" validate:"required"`

	// Data source
	SourceFilePath string `json:"source_file_path,omitempty"`
	SourceURL      string `json:"source_url,omitempty"`

	// Ordered list of processing steps
	Steps []ETLPipelineStepConfig `json:"steps" validate:"required,min=1,max=20"`

	// Final output destination
	Target *ETLTarget `json:"target,omitempty"`

	// If true, store intermediate results per step
	KeepIntermediates bool `json:"keep_intermediates"`

	Concurrency int `json:"concurrency" validate:"min=0,max=256"`
}

// ──────────────────────────────────────────────
// ETL Job Results
// ──────────────────────────────────────────────

// ETLJobResult contains the summary result of an ETL job.
type ETLJobResult struct {
	DatasetID       string             `json:"dataset_id"`
	TotalRecords    int64              `json:"total_records"`
	Processed       int64              `json:"processed"`
	Failed          int64              `json:"failed"`
	Skipped         int64              `json:"skipped"`
	DuplicatesFound int64              `json:"duplicates_found,omitempty"`
	DryRun          bool               `json:"dry_run,omitempty"`
	OutputLocation  string             `json:"output_location,omitempty"`
	ColumnStats     map[string]ColStat `json:"column_stats,omitempty"`
	Duration        string             `json:"duration"`

	// Duplicates preview — first N duplicate rows for the frontend dialog.
	// Each row includes a "_row_number" key with the 1-based original index.
	DuplicateRows       []map[string]string `json:"duplicate_rows,omitempty"`
	DuplicatesDatasetID string              `json:"duplicates_dataset_id,omitempty"`

	// Analysis contains a full re-analysis of the dataset after the ETL step.
	// Populated automatically so the frontend can track quality changes.
	Analysis *analyzer.DatasetAnalysis `json:"analysis,omitempty"`

	// CleanReport contains a detailed per-operation breakdown of what
	// was changed during cleaning, so the user can see exactly what happened.
	CleanReport *CleanReport `json:"clean_report,omitempty"`

	// DedupReport contains a detailed breakdown of what was found and removed
	// during deduplication.
	DedupReport *DedupReport `json:"dedup_report,omitempty"`

	// NormalizeReport contains the decomposition breakdown from database
	// normalization (1NF/2NF/3NF).
	NormalizeReport *NormalizeReport `json:"normalize_report,omitempty"`
}

// CleanReport is a detailed log of all cleaning actions taken.
type CleanReport struct {
	TotalCellsModified int64            `json:"total_cells_modified"`
	TotalRowsDropped   int64            `json:"total_rows_dropped"`
	ColumnsDropped     []string         `json:"columns_dropped,omitempty"`
	Operations         []CleanOpSummary `json:"operations"`
}

// CleanOpSummary describes the effect of a single cleaning operation.
type CleanOpSummary struct {
	Operation     string   `json:"operation"`
	Column        string   `json:"column"`
	CellsAffected int64    `json:"cells_affected"`
	Reason        string   `json:"reason,omitempty"`
	SampleBefore  []string `json:"sample_before,omitempty"` // up to 5 original values
	SampleAfter   []string `json:"sample_after,omitempty"`  // corresponding cleaned values
}

// DedupReport is a detailed log of deduplication actions.
type DedupReport struct {
	Strategy        string              `json:"strategy"`         // "exact" or "fuzzy"
	MatchColumns    []string            `json:"match_columns"`    // columns used for matching
	KeepStrategy    string              `json:"keep_strategy"`    // "first" | "last" | "most_complete"
	TotalGroups     int64               `json:"total_groups"`     // number of duplicate groups found
	TotalDuplicates int64               `json:"total_duplicates"` // total duplicate rows
	TotalKept       int64               `json:"total_kept"`       // rows kept after dedup
	Groups          []DedupGroupSummary `json:"groups,omitempty"` // first N groups for preview
}

// DedupGroupSummary describes one group of duplicate rows.
type DedupGroupSummary struct {
	MatchKey    string              `json:"match_key"`    // the key these rows share
	GroupSize   int                 `json:"group_size"`   // total rows in this group
	KeptIndex   int                 `json:"kept_index"`   // 1-based row number of the kept row
	DroppedRows []map[string]string `json:"dropped_rows"` // first 3 dropped rows (sample values)
}

// ColStat contains per-column statistics after processing.
type ColStat struct {
	NullCount   int64  `json:"null_count"`
	UniqueCount int64  `json:"unique_count"`
	MinValue    string `json:"min_value,omitempty"`
	MaxValue    string `json:"max_value,omitempty"`
}
