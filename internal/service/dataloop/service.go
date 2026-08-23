// Package dataloop implements the vehicle data closed loop: capture upload,
// manifest validation, shadow-mode triage and curated dataset assembly.
package dataloop

import (
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/repository"
)

// Service exposes the data closed loop use cases.
type Service struct {
	store    repository.Store
	clock    clock.Clock
	recorder *audit.Recorder
	logger   *logging.Logger
}

// Dependencies bundles the collaborators of the data loop service.
type Dependencies struct {
	Store    repository.Store
	Clock    clock.Clock
	Recorder *audit.Recorder
	Logger   *logging.Logger
}

// NewService builds the data loop service.
func NewService(dependencies Dependencies) *Service {
	source := dependencies.Clock
	if source == nil {
		source = clock.System{}
	}
	recorder := dependencies.Recorder
	if recorder == nil {
		recorder = audit.NewRecorder(source)
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = logging.New(nil, logging.LevelInfo)
	}
	return &Service{
		store:    dependencies.Store,
		clock:    source,
		recorder: recorder,
		logger:   logger,
	}
}

// Clock exposes the injected time source.
func (s *Service) Clock() clock.Clock { return s.clock }
