package httpapi

import (
	"time"

	"github.com/vance1852/drviercar/internal/domain"
)

// CampaignView is the JSON projection of a campaign.
type CampaignView struct {
	ID          int64   `json:"id"`
	Code        string  `json:"code"`
	City        string  `json:"city"`
	Status      string  `json:"status"`
	PlannedKm   float64 `json:"planned_km"`
	CommittedKm float64 `json:"committed_km"`
	RemainingKm float64 `json:"remaining_km"`
	WindowStart string  `json:"window_start"`
	WindowEnd   string  `json:"window_end"`
	OwnerID     int64   `json:"owner_id"`
	Version     int64   `json:"version"`
}

func newCampaignView(campaign *domain.Campaign) CampaignView {
	return CampaignView{
		ID:          campaign.ID,
		Code:        campaign.Code,
		City:        campaign.City,
		Status:      string(campaign.Status),
		PlannedKm:   campaign.PlannedKm,
		CommittedKm: campaign.CommittedKm,
		RemainingKm: campaign.RemainingKm(),
		WindowStart: campaign.WindowStart.Format(time.RFC3339),
		WindowEnd:   campaign.WindowEnd.Format(time.RFC3339),
		OwnerID:     campaign.OwnerID,
		Version:     campaign.Version,
	}
}

func newCampaignViews(campaigns []*domain.Campaign) []CampaignView {
	views := make([]CampaignView, 0, len(campaigns))
	for _, campaign := range campaigns {
		views = append(views, newCampaignView(campaign))
	}
	return views
}

// VehicleView is the JSON projection of a vehicle.
type VehicleView struct {
	ID            int64    `json:"id"`
	Plate         string   `json:"plate"`
	Autonomy      string   `json:"autonomy"`
	Status        string   `json:"status"`
	HomeDepot     string   `json:"home_depot"`
	OdometerKm    float64  `json:"odometer_km"`
	SensorProfile []string `json:"sensor_profile"`
	Version       int64    `json:"version"`
}

func newVehicleView(vehicle *domain.Vehicle) VehicleView {
	return VehicleView{
		ID:            vehicle.ID,
		Plate:         vehicle.Plate,
		Autonomy:      string(vehicle.Autonomy),
		Status:        string(vehicle.Status),
		HomeDepot:     vehicle.HomeDepot,
		OdometerKm:    vehicle.OdometerKm,
		SensorProfile: append([]string(nil), vehicle.SensorProfile...),
		Version:       vehicle.Version,
	}
}

func newVehicleViews(vehicles []*domain.Vehicle) []VehicleView {
	views := make([]VehicleView, 0, len(vehicles))
	for _, vehicle := range vehicles {
		views = append(views, newVehicleView(vehicle))
	}
	return views
}

// AssignmentView is the JSON projection of an assignment.
type AssignmentView struct {
	ID         int64   `json:"id"`
	CampaignID int64   `json:"campaign_id"`
	VehicleID  int64   `json:"vehicle_id"`
	OperatorID int64   `json:"operator_id"`
	Status     string  `json:"status"`
	PlannedKm  float64 `json:"planned_km"`
	ShiftStart string  `json:"shift_start"`
	ShiftEnd   string  `json:"shift_end"`
	Route      string  `json:"route"`
	Version    int64   `json:"version"`
}

func newAssignmentView(assignment *domain.Assignment) AssignmentView {
	return AssignmentView{
		ID:         assignment.ID,
		CampaignID: assignment.CampaignID,
		VehicleID:  assignment.VehicleID,
		OperatorID: assignment.OperatorID,
		Status:     string(assignment.Status),
		PlannedKm:  assignment.PlannedKm,
		ShiftStart: assignment.ShiftStart.Format(time.RFC3339),
		ShiftEnd:   assignment.ShiftEnd.Format(time.RFC3339),
		Route:      assignment.Route,
		Version:    assignment.Version,
	}
}

func newAssignmentViews(assignments []*domain.Assignment) []AssignmentView {
	views := make([]AssignmentView, 0, len(assignments))
	for _, assignment := range assignments {
		views = append(views, newAssignmentView(assignment))
	}
	return views
}

// DriveView is the JSON projection of a drive session.
type DriveView struct {
	ID            int64   `json:"id"`
	AssignmentID  int64   `json:"assignment_id"`
	VehicleID     int64   `json:"vehicle_id"`
	OperatorID    int64   `json:"operator_id"`
	Status        string  `json:"status"`
	AutoKm        float64 `json:"auto_km"`
	ManualKm      float64 `json:"manual_km"`
	TotalKm       float64 `json:"total_km"`
	TakeoverCount int     `json:"takeover_count"`
	StartedAt     string  `json:"started_at"`
	EndedAt       string  `json:"ended_at,omitempty"`
	Version       int64   `json:"version"`
}

func newDriveView(session *domain.DriveSession) DriveView {
	view := DriveView{
		ID:            session.ID,
		AssignmentID:  session.AssignmentID,
		VehicleID:     session.VehicleID,
		OperatorID:    session.OperatorID,
		Status:        string(session.Status),
		AutoKm:        session.AutoKm,
		ManualKm:      session.ManualKm,
		TotalKm:       session.TotalKm(),
		TakeoverCount: session.TakeoverCount,
		StartedAt:     session.StartedAt.Format(time.RFC3339),
		Version:       session.Version,
	}
	if session.EndedAt != nil {
		view.EndedAt = session.EndedAt.Format(time.RFC3339)
	}
	return view
}

// TakeoverView is the JSON projection of a takeover event.
type TakeoverView struct {
	ID          int64   `json:"id"`
	DriveID     int64   `json:"drive_id"`
	Category    string  `json:"category"`
	Severity    int     `json:"severity"`
	ManualKm    float64 `json:"manual_km"`
	Description string  `json:"description"`
	Critical    bool    `json:"critical"`
	Resolved    bool    `json:"resolved"`
	OccurredAt  string  `json:"occurred_at"`
}

func newTakeoverView(event *domain.TakeoverEvent) TakeoverView {
	return TakeoverView{
		ID:          event.ID,
		DriveID:     event.DriveID,
		Category:    string(event.Category),
		Severity:    event.Severity,
		ManualKm:    event.ManualKm,
		Description: event.Description,
		Critical:    event.Critical(),
		Resolved:    event.Resolved,
		OccurredAt:  event.OccurredAt.Format(time.RFC3339),
	}
}

func newTakeoverViews(events []*domain.TakeoverEvent) []TakeoverView {
	views := make([]TakeoverView, 0, len(events))
	for _, event := range events {
		views = append(views, newTakeoverView(event))
	}
	return views
}

// SettlementView is the JSON projection of a settlement.
type SettlementView struct {
	ID             int64   `json:"id"`
	CampaignID     int64   `json:"campaign_id"`
	AssignmentID   int64   `json:"assignment_id"`
	Status         string  `json:"status"`
	AutoKm         float64 `json:"auto_km"`
	ManualKm       float64 `json:"manual_km"`
	BillableKm     float64 `json:"billable_km"`
	PenaltyKm      float64 `json:"penalty_km"`
	CriticalEvents int     `json:"critical_events"`
	BusinessDay    string  `json:"business_day"`
	ApprovedBy     int64   `json:"approved_by"`
	Note           string  `json:"note"`
}

func newSettlementView(settlement *domain.Settlement) SettlementView {
	return SettlementView{
		ID:             settlement.ID,
		CampaignID:     settlement.CampaignID,
		AssignmentID:   settlement.AssignmentID,
		Status:         string(settlement.Status),
		AutoKm:         settlement.AutoKm,
		ManualKm:       settlement.ManualKm,
		BillableKm:     settlement.BillableKm,
		PenaltyKm:      settlement.PenaltyKm,
		CriticalEvents: settlement.CriticalEvents,
		BusinessDay:    settlement.BusinessDay,
		ApprovedBy:     settlement.ApprovedBy,
		Note:           settlement.Note,
	}
}

// BatchView is the JSON projection of a capture batch.
type BatchView struct {
	ID            int64  `json:"id"`
	VehicleID     int64  `json:"vehicle_id"`
	DriveID       int64  `json:"drive_id"`
	UploadKey     string `json:"upload_key"`
	Status        string `json:"status"`
	FrameCount    int    `json:"frame_count"`
	AcceptedCount int    `json:"accepted_count"`
	Manifest      string `json:"manifest"`
	UploadedAt    string `json:"uploaded_at"`
	ValidatedAt   string `json:"validated_at,omitempty"`
	RejectReason  string `json:"reject_reason,omitempty"`
	Version       int64  `json:"version"`
}

func newBatchView(batch *domain.CaptureBatch) BatchView {
	view := BatchView{
		ID:            batch.ID,
		VehicleID:     batch.VehicleID,
		DriveID:       batch.DriveID,
		UploadKey:     batch.UploadKey,
		Status:        string(batch.Status),
		FrameCount:    batch.FrameCount,
		AcceptedCount: batch.AcceptedCount,
		Manifest:      batch.Manifest,
		UploadedAt:    batch.UploadedAt.Format(time.RFC3339),
		RejectReason:  batch.RejectReason,
		Version:       batch.Version,
	}
	if batch.ValidatedAt != nil {
		view.ValidatedAt = batch.ValidatedAt.Format(time.RFC3339)
	}
	return view
}

func newBatchViews(batches []*domain.CaptureBatch) []BatchView {
	views := make([]BatchView, 0, len(batches))
	for _, batch := range batches {
		views = append(views, newBatchView(batch))
	}
	return views
}

// FrameView is the JSON projection of a capture frame.
type FrameView struct {
	ID           int64   `json:"id"`
	BatchID      int64   `json:"batch_id"`
	Sequence     int     `json:"sequence"`
	Sensor       string  `json:"sensor"`
	PayloadHash  string  `json:"payload_hash"`
	QualityScore float64 `json:"quality_score"`
	Status       string  `json:"status"`
	Reason       string  `json:"reason,omitempty"`
}

func newFrameViews(frames []*domain.CaptureFrame) []FrameView {
	views := make([]FrameView, 0, len(frames))
	for _, frame := range frames {
		views = append(views, FrameView{
			ID:           frame.ID,
			BatchID:      frame.BatchID,
			Sequence:     frame.Sequence,
			Sensor:       frame.Sensor,
			PayloadHash:  frame.PayloadHash,
			QualityScore: frame.QualityScore,
			Status:       string(frame.Status),
			Reason:       frame.Reason,
		})
	}
	return views
}

// TicketView is the JSON projection of a triage ticket.
type TicketView struct {
	ID          int64  `json:"id"`
	BatchID     int64  `json:"batch_id"`
	DriveID     int64  `json:"drive_id"`
	Status      string `json:"status"`
	Disposition string `json:"disposition,omitempty"`
	Severity    int    `json:"severity"`
	AssigneeID  int64  `json:"assignee_id"`
	OpenedAt    string `json:"opened_at"`
	DeadlineAt  string `json:"deadline_at"`
	Conclusion  string `json:"conclusion,omitempty"`
	Version     int64  `json:"version"`
}

func newTicketView(ticket *domain.TriageTicket) TicketView {
	return TicketView{
		ID:          ticket.ID,
		BatchID:     ticket.BatchID,
		DriveID:     ticket.DriveID,
		Status:      string(ticket.Status),
		Disposition: string(ticket.Disposition),
		Severity:    ticket.Severity,
		AssigneeID:  ticket.AssigneeID,
		OpenedAt:    ticket.OpenedAt.Format(time.RFC3339),
		DeadlineAt:  ticket.DeadlineAt.Format(time.RFC3339),
		Conclusion:  ticket.Conclusion,
		Version:     ticket.Version,
	}
}

func newTicketViews(tickets []*domain.TriageTicket) []TicketView {
	views := make([]TicketView, 0, len(tickets))
	for _, ticket := range tickets {
		views = append(views, newTicketView(ticket))
	}
	return views
}

// DatasetView is the JSON projection of a dataset.
type DatasetView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Purpose    string `json:"purpose,omitempty"`
	Status     string `json:"status"`
	FrameCount int    `json:"frame_count"`
	OwnerID    int64  `json:"owner_id"`
	SealDigest string `json:"seal_digest,omitempty"`
	Version    int64  `json:"version"`
}

func newDatasetView(dataset *domain.Dataset) DatasetView {
	return DatasetView{
		ID:         dataset.ID,
		Name:       dataset.Name,
		Purpose:    dataset.Purpose,
		Status:     string(dataset.Status),
		FrameCount: dataset.FrameCount,
		OwnerID:    dataset.OwnerID,
		SealDigest: dataset.SealDigest,
		Version:    dataset.Version,
	}
}
