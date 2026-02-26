package validator

import (
	"fmt"
	"strings"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ValidationError collects field-level validation errors.
type ValidationError struct {
	Fields map[string]string
}

func (v *ValidationError) Error() string {
	var parts []string
	for field, msg := range v.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", field, msg))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

func (v *ValidationError) Add(field, message string) {
	if v.Fields == nil {
		v.Fields = make(map[string]string)
	}
	v.Fields[field] = message
}

func (v *ValidationError) HasErrors() bool {
	return len(v.Fields) > 0
}

// ──────────────────────────────────────────────
// ETL Validation
// ──────────────────────────────────────────────

// ValidateETLImportParams validates ETLImportParams.
func ValidateETLImportParams(p *models.ETLImportParams) error {
	ve := &ValidationError{}

	if p.SourceFilePath == "" && p.SourceURL == "" {
		ve.Add("source", "either source_file_path or source_url is required")
	}
	if p.DatasetID == "" {
		ve.Add("dataset_id", "is required")
	}
	if p.BatchSize != 0 && (p.BatchSize < 1 || p.BatchSize > 100000) {
		ve.Add("batch_size", "must be between 1 and 100000")
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateETLCleanParams validates ETLCleanParams.
func ValidateETLCleanParams(p *models.ETLCleanParams) error {
	ve := &ValidationError{}

	if p.DatasetID == "" {
		ve.Add("dataset_id", "is required")
	}
	if len(p.Rules) == 0 {
		ve.Add("rules", "at least one cleaning rule is required")
	}
	for i, rule := range p.Rules {
		if rule.Operation == "" {
			ve.Add(fmt.Sprintf("rules[%d].operation", i), "is required")
		}
	}
	if p.NullHandling != "" {
		allowed := map[string]bool{
			"drop": true, "fill_default": true, "fill_mean": true,
			"fill_median": true, "fill_custom": true, "skip": true,
		}
		if !allowed[p.NullHandling] {
			ve.Add("null_handling", "must be one of: drop, fill_default, fill_mean, fill_median, fill_custom, skip")
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateETLNormalizeParams validates ETLNormalizeParams.
// At least one of rules or normal_form must be provided.
func ValidateETLNormalizeParams(p *models.ETLNormalizeParams) error {
	ve := &ValidationError{}

	if p.DatasetID == "" {
		ve.Add("dataset_id", "is required")
	}

	hasRules := len(p.Rules) > 0
	hasDBNorm := p.NormalForm > 0

	if !hasRules && !hasDBNorm {
		ve.Add("rules/normal_form", "at least one of rules or normal_form (1-3) is required")
	}

	// Validate value-level rules
	if hasRules {
		allowedOps := map[string]bool{
			"min_max_scale": true, "z_score": true, "email_normalize": true,
			"phone_format": true, "date_format": true, "enum_map": true,
			"url_normalize": true, "to_lowercase": true, "to_uppercase": true,
			"trim": true, "currency_format": true, "unit_convert": true,
		}
		for i, rule := range p.Rules {
			if rule.Column == "" {
				ve.Add(fmt.Sprintf("rules[%d].column", i), "is required")
			}
			if rule.Operation == "" {
				ve.Add(fmt.Sprintf("rules[%d].operation", i), "is required")
			} else if !allowedOps[rule.Operation] {
				ve.Add(fmt.Sprintf("rules[%d].operation", i),
					"must be one of: min_max_scale, z_score, email_normalize, phone_format, date_format, enum_map, url_normalize, to_lowercase, to_uppercase, trim, currency_format, unit_convert")
			}
		}
	}

	// Validate database normalization
	if hasDBNorm {
		if p.NormalForm < 1 || p.NormalForm > 3 {
			ve.Add("normal_form", "must be 1, 2, or 3")
		}
		if p.NormalForm >= 2 && p.PrimaryKeyColumn == "" {
			ve.Add("primary_key_column", "is required for 2NF and 3NF")
		}
		if p.NormalForm >= 2 && len(p.CategoricalColumns) == 0 {
			ve.Add("categorical_columns", "at least one categorical column is required for 2NF/3NF")
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateETLDeduplicateParams validates ETLDeduplicateParams.
func ValidateETLDeduplicateParams(p *models.ETLDeduplicateParams) error {
	ve := &ValidationError{}

	if p.DatasetID == "" {
		ve.Add("dataset_id", "is required")
	}
	if len(p.MatchColumns) == 0 {
		ve.Add("match_columns", "at least one column is required")
	}
	if p.Strategy == "" {
		ve.Add("strategy", "is required")
	} else if p.Strategy != "exact" && p.Strategy != "fuzzy" {
		ve.Add("strategy", "must be 'exact' or 'fuzzy'")
	}
	if p.Strategy == "fuzzy" && (p.FuzzyThreshold < 0 || p.FuzzyThreshold > 1) {
		ve.Add("fuzzy_threshold", "must be between 0.0 and 1.0")
	}
	if p.KeepStrategy == "" {
		ve.Add("keep_strategy", "is required")
	} else {
		allowed := map[string]bool{"first": true, "last": true, "most_complete": true}
		if !allowed[p.KeepStrategy] {
			ve.Add("keep_strategy", "must be one of: first, last, most_complete")
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateETLPipelineParams validates ETLPipelineParams.
func ValidateETLPipelineParams(p *models.ETLPipelineParams) error {
	ve := &ValidationError{}

	if p.Name == "" {
		ve.Add("name", "is required")
	}
	if p.SourceFilePath == "" && p.SourceURL == "" {
		ve.Add("source", "either source_file_path or source_url is required")
	}
	if len(p.Steps) == 0 {
		ve.Add("steps", "at least one step is required")
	}
	if len(p.Steps) > 20 {
		ve.Add("steps", "maximum 20 steps allowed")
	}
	for i, step := range p.Steps {
		allowed := map[string]bool{"import": true, "clean": true, "normalize": true, "deduplicate": true}
		if !allowed[step.Action] {
			ve.Add(fmt.Sprintf("steps[%d].action", i), "must be one of: import, clean, normalize, deduplicate")
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ──────────────────────────────────────────────
// Job Request Validation
// ──────────────────────────────────────────────

// ValidateCreateJobRequest validates the common job wrapper.
func ValidateCreateJobRequest(r *models.CreateJobRequest) error {
	ve := &ValidationError{}

	if r.Priority != 0 && !r.Priority.IsValid() {
		ve.Add("priority", "must be between 1 and 10")
	}
	if r.MaxRetries < 0 || r.MaxRetries > 10 {
		ve.Add("max_retries", "must be between 0 and 10")
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}
