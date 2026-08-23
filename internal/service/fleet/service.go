// Package fleet implements the road-test orchestration chain: campaign
// planning, vehicle and safety operator assignment, on-road execution and
// mileage settlement.
package fleet

import (
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/idem"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/repository"
)

// Service exposes the fleet operations use cases.
type Service struct {
	store    repository.Store
	clock    clock.Clock
	recorder *audit.Recorder
	idem     *idem.Manager
	logger   *logging.Logger
}

// Dependencies bundles the collaborators of the fleet service.
type Dependencies struct {
	Store       repository.Store
	Clock       clock.Clock
	Recorder    *audit.Recorder
	Idempotency *idem.Manager
	Logger      *logging.Logger
}

// NewService builds the fleet service.
func NewService(dependencies Dependencies) *Service {
	source := dependencies.Clock
	if source == nil {
		source = clock.System{}
	}
	recorder := dependencies.Recorder
	if recorder == nil {
		recorder = audit.NewRecorder(source)
	}
	manager := dependencies.Idempotency
	if manager == nil {
		manager = idem.NewManager(source, 0)
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = logging.New(nil, logging.LevelInfo)
	}
	return &Service{
		store:    dependencies.Store,
		clock:    source,
		recorder: recorder,
		idem:     manager,
		logger:   logger,
	}
}

// Clock exposes the injected time source for handlers that must align business
// timestamps with the service.
func (s *Service) Clock() clock.Clock { return s.clock }
