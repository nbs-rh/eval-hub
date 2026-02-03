package api

import "time"

type ErrorMessage struct {
	Message     string `json:"message"`
	MessageCode string `json:"message_code"`
}

type RunStatusInternal struct {
	ProviderID      string         `json:"provider_id"`
	BenchmarkID     string         `json:"benchmark_id"`
	BenchmarkName   string         `json:"benchmark_name,omitempty"`
	Status          State          `json:"status,omitempty"`
	Metrics         map[string]any `json:"metrics,omitempty"`
	Artifacts       map[string]any `json:"artifacts,omitempty"`
	ErrorMessage    *ErrorMessage  `json:"error_message,omitempty"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	DurationSeconds int64          `json:"duration_seconds,omitempty"`
	MLFlowRunID     string         `json:"mlflow_run_id,omitempty"`
}
