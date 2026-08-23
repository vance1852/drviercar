// Package testsupport builds a fully wired platform instance on top of a
// throwaway SQLite database. It is used by the integration and HTTP tests and
// returns errors instead of depending on the testing package.
package testsupport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/idem"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/service/auth"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	sqlitestore "github.com/vance1852/drviercar/internal/storage/sqlite"
	"github.com/vance1852/drviercar/internal/worker"
)

// Harness bundles a live store and the wired services.
type Harness struct {
	Store      *sqlitestore.Store
	Clock      *clock.Fixed
	Auth       *auth.Service
	Fleet      *fleet.Service
	DataLoop   *dataloop.Service
	Dispatcher *worker.Dispatcher
	Maintenance *worker.Maintenance
	Recorder   *audit.Recorder
	Path       string
}

// Anchor is the deterministic base instant used by the tests.
var Anchor = time.Date(2026, 3, 2, 6, 0, 0, 0, clock.OperationsZone)

// New builds a harness whose database lives inside directory.
func New(directory string) (*Harness, error) {
	path := filepath.Join(directory, "drviercar-test.sqlite")
	options := sqlitestore.DefaultOptions(path)
	options.MaxOpenConns = 4
	store, err := sqlitestore.Open(context.Background(), options)
	if err != nil {
		return nil, err
	}
	fixed := clock.NewFixed(Anchor)
	recorder := audit.NewRecorder(fixed)
	logger := logging.New(discard{}, logging.LevelError)
	harness := &Harness{
		Store:    store,
		Clock:    fixed,
		Recorder: recorder,
		Path:     path,
		Auth: auth.NewService(store, fixed, recorder, auth.Config{
			SessionTTL: 2 * time.Hour,
		}),
	}
	harness.Fleet = fleet.NewService(fleet.Dependencies{
		Store:       store,
		Clock:       fixed,
		Recorder:    recorder,
		Idempotency: idem.NewManager(fixed, time.Hour),
		Logger:      logger,
	})
	harness.DataLoop = dataloop.NewService(dataloop.Dependencies{
		Store:    store,
		Clock:    fixed,
		Recorder: recorder,
		Logger:   logger,
	})
	harness.Dispatcher = worker.NewDispatcher(store, fixed, logger, worker.Config{
		Interval:    10 * time.Millisecond,
		BatchSize:   4,
		BaseBackoff: time.Second,
		MaxBackoff:  time.Minute,
	})
	harness.Maintenance = worker.NewMaintenance(store, fixed, recorder)
	harness.Maintenance.RegisterAll(harness.Dispatcher)
	return harness, nil
}

// Reopen closes the store and opens the same file again, which is how the tests
// prove that persisted state survives a restart.
func (h *Harness) Reopen() error {
	if err := h.Store.Close(); err != nil {
		return err
	}
	options := sqlitestore.DefaultOptions(h.Path)
	store, err := sqlitestore.Open(context.Background(), options)
	if err != nil {
		return err
	}
	h.Store = store
	h.Auth = auth.NewService(store, h.Clock, h.Recorder, auth.Config{SessionTTL: 2 * time.Hour})
	logger := logging.New(discard{}, logging.LevelError)
	h.Fleet = fleet.NewService(fleet.Dependencies{
		Store:       store,
		Clock:       h.Clock,
		Recorder:    h.Recorder,
		Idempotency: idem.NewManager(h.Clock, time.Hour),
		Logger:      logger,
	})
	h.DataLoop = dataloop.NewService(dataloop.Dependencies{
		Store:    store,
		Clock:    h.Clock,
		Recorder: h.Recorder,
		Logger:   logger,
	})
	h.Maintenance = worker.NewMaintenance(store, h.Clock, h.Recorder)
	return nil
}

// Close releases the store.
func (h *Harness) Close() error {
	return h.Store.Close()
}

// Actors bundles the two seeded principals.
type Actors struct {
	Admin        domain.Principal
	AdminToken   string
	Operator     domain.Principal
	OperatorToken string
	SecondOperator domain.Principal
	SecondToken  string
}

// SeedActors registers one fleet administrator and two safety operators and logs
// each of them in.
func (h *Harness) SeedActors(ctx context.Context) (*Actors, error) {
	actors := &Actors{}
	admin, err := h.register(ctx, "fleet-admin", "admin-secret-1", domain.RoleFleetAdmin)
	if err != nil {
		return nil, err
	}
	actors.Admin, actors.AdminToken = admin.principal, admin.token

	first, err := h.register(ctx, "safety-lin", "driver-secret-1", domain.RoleSafetyOperator)
	if err != nil {
		return nil, err
	}
	actors.Operator, actors.OperatorToken = first.principal, first.token

	second, err := h.register(ctx, "safety-zhao", "driver-secret-2", domain.RoleSafetyOperator)
	if err != nil {
		return nil, err
	}
	actors.SecondOperator, actors.SecondToken = second.principal, second.token
	return actors, nil
}

type seeded struct {
	principal domain.Principal
	token     string
}

func (h *Harness) register(
	ctx context.Context,
	username, password string,
	role domain.Role,
) (seeded, error) {
	if _, err := h.Auth.Register(ctx, auth.RegisterInput{
		Username:    username,
		DisplayName: username,
		Password:    password,
		Role:        role,
	}); err != nil {
		return seeded{}, err
	}
	login, err := h.Auth.Login(ctx, auth.Credentials{Username: username, Password: password})
	if err != nil {
		return seeded{}, err
	}
	principal, err := h.Auth.Authenticate(ctx, login.Token)
	if err != nil {
		return seeded{}, err
	}
	return seeded{principal: principal, token: login.Token}, nil
}

// SeedCampaign creates a scheduled campaign with a generous window.
func (h *Harness) SeedCampaign(
	ctx context.Context,
	admin domain.Principal,
	code string,
	plannedKm float64,
) (*domain.Campaign, error) {
	campaign, err := h.Fleet.CreateCampaign(ctx, admin, fleet.CreateCampaignInput{
		Code:        code,
		City:        "shanghai-jiading",
		PlannedKm:   plannedKm,
		WindowStart: Anchor.Add(-time.Hour),
		WindowEnd:   Anchor.Add(72 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	return h.Fleet.TransitionCampaign(ctx, admin, campaign.ID, domain.CampaignScheduled, "ready for road test")
}

// SeedVehicle registers an idle vehicle.
func (h *Harness) SeedVehicle(
	ctx context.Context,
	admin domain.Principal,
	plate string,
	level domain.AutonomyLevel,
) (*domain.Vehicle, error) {
	return h.Fleet.RegisterVehicle(ctx, admin, fleet.RegisterVehicleInput{
		Plate:         plate,
		Autonomy:      level,
		HomeDepot:     "jiading-depot",
		SensorProfile: []string{"lidar-front", "camera-ring", "radar-rear"},
	})
}

// FrameSpec describes a synthetic capture frame.
type FrameSpec struct {
	Sequence int
	Sensor   string
	Quality  float64
}

// BuildFrames turns specs into upload frames with deterministic payload hashes.
func BuildFrames(specs []FrameSpec, capturedAt time.Time) []dataloop.FrameInput {
	frames := make([]dataloop.FrameInput, 0, len(specs))
	for _, spec := range specs {
		frames = append(frames, dataloop.FrameInput{
			Sequence:     spec.Sequence,
			Sensor:       spec.Sensor,
			PayloadHash:  PayloadHash(spec.Sequence, spec.Sensor),
			QualityScore: spec.Quality,
			CapturedAt:   capturedAt,
		})
	}
	return frames
}

// PayloadHash derives a stable synthetic payload digest.
func PayloadHash(sequence int, sensor string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("payload:%d:%s", sequence, sensor)))
	return hex.EncodeToString(digest[:])
}

// Manifest computes the manifest digest of the supplied upload frames.
func Manifest(frames []dataloop.FrameInput) string {
	converted := make([]*domain.CaptureFrame, 0, len(frames))
	for _, frame := range frames {
		converted = append(converted, &domain.CaptureFrame{
			Sequence:     frame.Sequence,
			Sensor:       frame.Sensor,
			PayloadHash:  frame.PayloadHash,
			QualityScore: frame.QualityScore,
		})
	}
	return domain.ManifestDigest(converted)
}

type discard struct{}

func (discard) Write(payload []byte) (int, error) { return len(payload), nil }
