package runtimes

import (
	"log/slog"

	"github.com/eval-hub/eval-hub/internal/abstractions"
	"github.com/eval-hub/eval-hub/internal/config"
	"github.com/eval-hub/eval-hub/internal/runtimes/k8s"
	"github.com/eval-hub/eval-hub/internal/runtimes/local"
)

func NewRuntime(logger *slog.Logger, serviceConfig *config.Config) (abstractions.Runtime, error) {
	var runtime abstractions.Runtime
	var err error

	if serviceConfig.Service.LocalMode {
		runtime, err = local.NewLocalRuntime(logger)
	} else {
		runtime, err = k8s.NewK8sRuntime(logger)
	}

	return runtime, err
}
