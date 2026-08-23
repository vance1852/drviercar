// Package repository declares the persistence contracts consumed by the
// services. Implementations live in internal/storage and must never leak SQL
// types across this boundary.
package repository

import (
	"context"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
)

// OperatorRepository persists platform operators.
type OperatorRepository interface {
	Create(ctx context.Context, operator *domain.Operator) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Operator, error)
	ByUsername(ctx context.Context, username string) (*domain.Operator, error)
	SetActive(ctx context.Context, id int64, active bool) error
}

// SessionRepository persists login sessions.
type SessionRepository interface {
	Create(ctx context.Context, session *domain.Session) (int64, error)
	ByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error)
	Revoke(ctx context.Context, tokenHash string, revokedAt time.Time) error
	RevokeAllForOperator(ctx context.Context, operatorID int64, revokedAt time.Time) (int, error)
	TouchLastSeen(ctx context.Context, id int64, seenAt time.Time) error
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// CampaignFilter narrows a campaign list query.
type CampaignFilter struct {
	City      string
	Statuses  []domain.CampaignStatus
	OwnerID   int64
	StartFrom *time.Time
	StartTo   *time.Time
	Page      domain.PageRequest
}

// CampaignRepository persists road-test campaigns.
type CampaignRepository interface {
	Create(ctx context.Context, campaign *domain.Campaign) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Campaign, error)
	ByCode(ctx context.Context, code string) (*domain.Campaign, error)
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.CampaignStatus, closedAt *time.Time, reason string) error
	CommitKm(ctx context.Context, id int64, expectedVersion int64, deltaKm float64) error
	List(ctx context.Context, filter CampaignFilter) ([]*domain.Campaign, int, error)
}

// VehicleFilter narrows a vehicle list query.
type VehicleFilter struct {
	Depot    string
	Statuses []domain.VehicleStatus
	Autonomy domain.AutonomyLevel
	Page     domain.PageRequest
}

// VehicleRepository persists fleet vehicles.
type VehicleRepository interface {
	Create(ctx context.Context, vehicle *domain.Vehicle) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Vehicle, error)
	ByPlate(ctx context.Context, plate string) (*domain.Vehicle, error)
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.VehicleStatus) error
	AddOdometer(ctx context.Context, id int64, expectedVersion int64, deltaKm float64) error
	List(ctx context.Context, filter VehicleFilter) ([]*domain.Vehicle, int, error)
}

// AssignmentFilter narrows an assignment list query.
type AssignmentFilter struct {
	CampaignID int64
	VehicleID  int64
	OperatorID int64
	Statuses   []domain.AssignmentStatus
	Page       domain.PageRequest
}

// AssignmentRepository persists campaign shift assignments.
type AssignmentRepository interface {
	Create(ctx context.Context, assignment *domain.Assignment) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Assignment, error)
	ByIdempotencyKey(ctx context.Context, key string) (*domain.Assignment, error)
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.AssignmentStatus, closedAt *time.Time) error
	OpenForVehicle(ctx context.Context, vehicleID int64) ([]*domain.Assignment, error)
	OpenForOperator(ctx context.Context, operatorID int64) ([]*domain.Assignment, error)
	CountOpenByCampaign(ctx context.Context, campaignID int64) (int, error)
	CompletedByCampaign(ctx context.Context, campaignID int64, limit int) ([]*domain.Assignment, error)
	List(ctx context.Context, filter AssignmentFilter) ([]*domain.Assignment, int, error)
}

// DriveRepository persists drive sessions and takeover events.
type DriveRepository interface {
	Create(ctx context.Context, session *domain.DriveSession) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.DriveSession, error)
	ActiveByAssignment(ctx context.Context, assignmentID int64) (*domain.DriveSession, error)
	AddMileage(ctx context.Context, id int64, expectedVersion int64, autoKm, manualKm float64) error
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.DriveStatus, endedAt *time.Time) error
	AppendTakeover(ctx context.Context, event *domain.TakeoverEvent) (int64, error)
	ResolveTakeover(ctx context.Context, id int64) error
	TakeoversByDrive(ctx context.Context, driveID int64) ([]*domain.TakeoverEvent, error)
	CountUnresolvedCritical(ctx context.Context, driveID int64) (int, error)
	ByAssignment(ctx context.Context, assignmentID int64) ([]*domain.DriveSession, error)
}

// SettlementRepository persists mileage settlements.
type SettlementRepository interface {
	Create(ctx context.Context, settlement *domain.Settlement) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Settlement, error)
	ByAssignment(ctx context.Context, assignmentID int64) (*domain.Settlement, error)
	Approve(ctx context.Context, id int64, approvedBy int64, note string) error
	ByCampaign(ctx context.Context, campaignID int64) ([]*domain.Settlement, error)
	// CountNotApprovedByCampaign reports how many completed shifts of a
	// campaign still lack an approved settlement. It counts assignments whose
	// status is completed but that have no settlement in the approved state,
	// so it is unaffected by list pagination and is the guard used when a
	// campaign is about to be closed.
	CountNotApprovedByCampaign(ctx context.Context, campaignID int64) (int, error)
	SumBillableKm(ctx context.Context, campaignID int64) (float64, error)
}

// CaptureFilter narrows a capture batch list query.
type CaptureFilter struct {
	VehicleID int64
	DriveID   int64
	Statuses  []domain.BatchStatus
	Page      domain.PageRequest
}

// CaptureRepository persists capture batches and frames.
type CaptureRepository interface {
	CreateBatch(ctx context.Context, batch *domain.CaptureBatch) (int64, error)
	BatchByID(ctx context.Context, id int64) (*domain.CaptureBatch, error)
	BatchByUploadKey(ctx context.Context, uploadKey string) (*domain.CaptureBatch, error)
	UpdateBatchStatus(ctx context.Context, id int64, expectedVersion int64, status domain.BatchStatus, validatedAt *time.Time, acceptedCount int, reason string) error
	AppendFrames(ctx context.Context, batchID int64, frames []*domain.CaptureFrame) error
	FramesByBatch(ctx context.Context, batchID int64) ([]*domain.CaptureFrame, error)
	FrameByID(ctx context.Context, id int64) (*domain.CaptureFrame, error)
	UpdateFrameStatus(ctx context.Context, id int64, status domain.FrameStatus, reason string) error
	ListBatches(ctx context.Context, filter CaptureFilter) ([]*domain.CaptureBatch, int, error)
}

// TicketFilter narrows a triage ticket list query.
type TicketFilter struct {
	BatchID    int64
	AssigneeID int64
	Statuses   []domain.TicketStatus
	DueBefore  *time.Time
	Page       domain.PageRequest
}

// TriageRepository persists shadow-mode triage tickets.
type TriageRepository interface {
	Create(ctx context.Context, ticket *domain.TriageTicket) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.TriageTicket, error)
	ByBatch(ctx context.Context, batchID int64) ([]*domain.TriageTicket, error)
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.TicketStatus, disposition domain.Disposition, conclusion string, disposedAt *time.Time) error
	Assign(ctx context.Context, id int64, expectedVersion int64, assigneeID int64) error
	CountPendingByDrive(ctx context.Context, driveID int64) (int, error)
	List(ctx context.Context, filter TicketFilter) ([]*domain.TriageTicket, int, error)
}

// DatasetRepository persists curated datasets.
type DatasetRepository interface {
	Create(ctx context.Context, dataset *domain.Dataset) (int64, error)
	ByID(ctx context.Context, id int64) (*domain.Dataset, error)
	ByName(ctx context.Context, name string) (*domain.Dataset, error)
	AddMember(ctx context.Context, datasetID, frameID int64) error
	RemoveMember(ctx context.Context, datasetID, frameID int64) error
	MemberFrameIDs(ctx context.Context, datasetID int64) ([]int64, error)
	UpdateStatus(ctx context.Context, id int64, expectedVersion int64, status domain.DatasetStatus, sealedAt *time.Time, digest string) error
	SyncFrameCount(ctx context.Context, id int64) (int, error)
}

// AuditRepository persists audit events.
type AuditRepository interface {
	Append(ctx context.Context, event *domain.AuditEvent) (int64, error)
	ByObject(ctx context.Context, objectType string, objectID int64) ([]*domain.AuditEvent, error)
	ByRequestID(ctx context.Context, requestID string) ([]*domain.AuditEvent, error)
	Count(ctx context.Context) (int, error)
}

// IdempotencyRecord is a stored request fingerprint.
type IdempotencyRecord struct {
	Key          string
	Method       string
	Path         string
	OperatorID   int64
	RequestHash  string
	ResponseBody string
	CreatedAt    time.Time
}

// IdempotencyRepository persists idempotency keys scoped by method, path and
// operator so that the same key cannot short-circuit a different endpoint.
type IdempotencyRepository interface {
	Reserve(ctx context.Context, record IdempotencyRecord) (existing *IdempotencyRecord, err error)
	Complete(ctx context.Context, key, method, path string, operatorID int64, responseBody string) error
	DeleteOlderThan(ctx context.Context, before time.Time) (int, error)
}

// Job is a background work item.
type Job struct {
	ID          int64
	Kind        string
	Payload     string
	Status      string
	Attempts    int
	MaxAttempts int
	NextRunAt   time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Version     int64
}

// Job statuses.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobDead      = "dead"
)

// JobRepository persists the background job queue.
type JobRepository interface {
	Enqueue(ctx context.Context, job *Job) (int64, error)
	ByID(ctx context.Context, id int64) (*Job, error)
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]*Job, error)
	MarkSucceeded(ctx context.Context, id int64) error
	MarkRetry(ctx context.Context, id int64, nextRunAt time.Time, lastError string) error
	MarkDead(ctx context.Context, id int64, lastError string) error
	CountByStatus(ctx context.Context, status string) (int, error)
}

// Registry groups every repository bound to one database handle or transaction.
type Registry struct {
	Operators     OperatorRepository
	Sessions      SessionRepository
	Campaigns     CampaignRepository
	Vehicles      VehicleRepository
	Assignments   AssignmentRepository
	Drives        DriveRepository
	Settlements   SettlementRepository
	Captures      CaptureRepository
	Triage        TriageRepository
	Datasets      DatasetRepository
	Audit         AuditRepository
	Idempotency   IdempotencyRepository
	Jobs          JobRepository
}

// TxRunner executes fn inside a single database transaction. The callback
// receives a registry bound to that transaction; returning an error rolls back
// every write performed through it.
type TxRunner interface {
	WithTx(ctx context.Context, fn func(ctx context.Context, tx *Registry) error) error
}

// Store is the full persistence surface used by the services.
type Store interface {
	TxRunner
	Repos() *Registry
	Ping(ctx context.Context) error
}
