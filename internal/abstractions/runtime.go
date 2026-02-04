package abstractions

import (
	"github.com/eval-hub/eval-hub/internal/executioncontext"
	"github.com/eval-hub/eval-hub/pkg/api"
)

// Runtime interface defines the methods for running evaluation jobs. Concrete implemementation
// hold the specific aspects of various runtimes (i.e. K8s, local, etc.). No other places in the code should
// be pointing directly to K8s or other runtime specific details.
type Runtime interface {
	Name() string
	RunEvaluationJob(ctx *executioncontext.ExecutionContext, evaluation *api.EvaluationJobResource, storage *Storage) error
}
