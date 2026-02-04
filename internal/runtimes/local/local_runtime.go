package local

import (
	"log/slog"

	"github.com/eval-hub/eval-hub/internal/abstractions"
	"github.com/eval-hub/eval-hub/internal/executioncontext"
	"github.com/eval-hub/eval-hub/pkg/api"
)

type LocalRuntime struct {
	logger *slog.Logger
}

func NewLocalRuntime(logger *slog.Logger) (abstractions.Runtime, error) {
	return &LocalRuntime{logger: logger}, nil
}

func (r *LocalRuntime) RunEvaluationJob(_ *executioncontext.ExecutionContext, evaluation *api.EvaluationJobResource, storage *abstractions.Storage) error {
	return nil
}

func (r *LocalRuntime) Name() string {
	return "local"
}
