package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/fleet"
)

type createCampaignRequest struct {
	Code        string  `json:"code"`
	City        string  `json:"city"`
	PlannedKm   float64 `json:"planned_km"`
	WindowStart string  `json:"window_start"`
	WindowEnd   string  `json:"window_end"`
}

func (rt *Router) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request createCampaignRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	start, err := parseMoment("window_start", request.WindowStart)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	end, err := parseMoment("window_end", request.WindowEnd)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	campaign, err := rt.fleet.CreateCampaign(r.Context(), principal, fleet.CreateCampaignInput{
		Code:        request.Code,
		City:        request.City,
		PlannedKm:   request.PlannedKm,
		WindowStart: start,
		WindowEnd:   end,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newCampaignView(campaign))
}

type transitionRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (rt *Router) handleTransitionCampaign(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	campaignID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request transitionRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	campaign, err := rt.fleet.TransitionCampaign(r.Context(), principal, campaignID,
		domain.CampaignStatus(strings.TrimSpace(request.Status)), strings.TrimSpace(request.Reason))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newCampaignView(campaign))
}

func (rt *Router) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	campaignID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	campaign, err := rt.fleet.GetCampaign(r.Context(), campaignID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newCampaignView(campaign))
}

func (rt *Router) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	page, err := PageFromQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.CampaignFilter{
		City: strings.TrimSpace(r.URL.Query().Get("city")),
		Page: page,
	}
	for _, status := range splitQueryList(r.URL.Query().Get("status")) {
		filter.Statuses = append(filter.Statuses, domain.CampaignStatus(status))
	}
	result, err := rt.fleet.ListCampaigns(r.Context(), filter)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListEnvelope{Items: newCampaignViews(result.Items), Meta: result.Meta})
}

type registerVehicleRequest struct {
	Plate         string   `json:"plate"`
	Autonomy      string   `json:"autonomy"`
	HomeDepot     string   `json:"home_depot"`
	SensorProfile []string `json:"sensor_profile"`
}

func (rt *Router) handleRegisterVehicle(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request registerVehicleRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	vehicle, err := rt.fleet.RegisterVehicle(r.Context(), principal, fleet.RegisterVehicleInput{
		Plate:         request.Plate,
		Autonomy:      domain.AutonomyLevel(strings.TrimSpace(request.Autonomy)),
		HomeDepot:     request.HomeDepot,
		SensorProfile: request.SensorProfile,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newVehicleView(vehicle))
}

func (rt *Router) handleListVehicles(w http.ResponseWriter, r *http.Request) {
	page, err := PageFromQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.VehicleFilter{
		Depot:    strings.TrimSpace(r.URL.Query().Get("depot")),
		Autonomy: domain.AutonomyLevel(strings.TrimSpace(r.URL.Query().Get("autonomy"))),
		Page:     page,
	}
	for _, status := range splitQueryList(r.URL.Query().Get("status")) {
		filter.Statuses = append(filter.Statuses, domain.VehicleStatus(status))
	}
	result, err := rt.fleet.ListVehicles(r.Context(), filter)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListEnvelope{Items: newVehicleViews(result.Items), Meta: result.Meta})
}

type createAssignmentRequest struct {
	CampaignID int64   `json:"campaign_id"`
	VehicleID  int64   `json:"vehicle_id"`
	OperatorID int64   `json:"operator_id"`
	PlannedKm  float64 `json:"planned_km"`
	ShiftStart string  `json:"shift_start"`
	ShiftEnd   string  `json:"shift_end"`
	Route      string  `json:"route"`
}

func (rt *Router) handleCreateAssignment(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		WriteError(w, r, apperr.Invalidf("idempotency_key_required",
			"创建排班必须提供 Idempotency-Key 请求头"))
		return
	}
	var request createAssignmentRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	start, err := parseMoment("shift_start", request.ShiftStart)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	end, err := parseMoment("shift_end", request.ShiftEnd)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assignment, err := rt.fleet.CreateAssignment(r.Context(), principal, fleet.CreateAssignmentInput{
		CampaignID:     request.CampaignID,
		VehicleID:      request.VehicleID,
		OperatorID:     request.OperatorID,
		PlannedKm:      request.PlannedKm,
		ShiftStart:     start,
		ShiftEnd:       end,
		Route:          request.Route,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newAssignmentView(assignment))
}

func (rt *Router) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	page, err := PageFromQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	campaignID, err := QueryInt(r, "campaign_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	vehicleID, err := QueryInt(r, "vehicle_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.AssignmentFilter{
		CampaignID: int64(campaignID),
		VehicleID:  int64(vehicleID),
		Page:       page,
	}
	for _, status := range splitQueryList(r.URL.Query().Get("status")) {
		filter.Statuses = append(filter.Statuses, domain.AssignmentStatus(status))
	}
	result, err := rt.fleet.ListAssignments(r.Context(), filter)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListEnvelope{Items: newAssignmentViews(result.Items), Meta: result.Meta})
}

func (rt *Router) handleGetAssignment(w http.ResponseWriter, r *http.Request) {
	assignmentID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assignment, err := rt.fleet.GetAssignment(r.Context(), assignmentID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newAssignmentView(assignment))
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func (rt *Router) handleAbortAssignment(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assignmentID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request reasonRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	assignment, err := rt.fleet.AbortAssignment(r.Context(), principal, assignmentID, request.Reason)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newAssignmentView(assignment))
}

type batchAbortRequest struct {
	AssignmentIDs []int64 `json:"assignment_ids"`
	Reason        string  `json:"reason"`
}

func (rt *Router) handleBatchAbortAssignments(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request batchAbortRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	outcome, err := rt.fleet.BatchAbortAssignments(r.Context(), principal,
		request.AssignmentIDs, request.Reason)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, outcome)
}

func (rt *Router) handleStartDrive(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assignmentID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := rt.fleet.StartDrive(r.Context(), principal, assignmentID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newDriveView(session))
}

type mileageRequest struct {
	AutoKm   float64 `json:"auto_km"`
	ManualKm float64 `json:"manual_km"`
}

func (rt *Router) handleReportMileage(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	driveID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request mileageRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := rt.fleet.ReportMileage(r.Context(), principal, fleet.MileageReport{
		DriveID:  driveID,
		AutoKm:   request.AutoKm,
		ManualKm: request.ManualKm,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newDriveView(session))
}

type takeoverRequest struct {
	Category    string  `json:"category"`
	Severity    int     `json:"severity"`
	ManualKm    float64 `json:"manual_km"`
	Description string  `json:"description"`
}

func (rt *Router) handleReportTakeover(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	driveID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request takeoverRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	event, err := rt.fleet.ReportTakeover(r.Context(), principal, fleet.TakeoverReport{
		DriveID:     driveID,
		Category:    domain.TakeoverCategory(strings.TrimSpace(request.Category)),
		Severity:    request.Severity,
		ManualKm:    request.ManualKm,
		Description: request.Description,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newTakeoverView(event))
}

type noteRequest struct {
	Note string `json:"note"`
}

func (rt *Router) handleResolveTakeover(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	takeoverID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request noteRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := rt.fleet.ResolveTakeover(r.Context(), principal, takeoverID, request.Note); err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"resolved": true})
}

func (rt *Router) handleCloseDrive(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	driveID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := rt.fleet.CloseDrive(r.Context(), principal, driveID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newDriveView(session))
}

func (rt *Router) handleGetDrive(w http.ResponseWriter, r *http.Request) {
	driveID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	detail, err := rt.fleet.DescribeDrive(r.Context(), driveID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"drive":     newDriveView(detail.Session),
		"takeovers": newTakeoverViews(detail.Takeovers),
	})
}

func (rt *Router) handleSettleAssignment(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assignmentID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	settlement, err := rt.fleet.SettleAssignment(r.Context(), principal, assignmentID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newSettlementView(settlement))
}

func (rt *Router) handleApproveSettlement(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	settlementID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request noteRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	settlement, err := rt.fleet.ApproveSettlement(r.Context(), principal, settlementID, request.Note)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newSettlementView(settlement))
}

func (rt *Router) handleCampaignSettlements(w http.ResponseWriter, r *http.Request) {
	campaignID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	summary, err := rt.fleet.SummariseCampaignSettlements(r.Context(), campaignID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, summary)
}

func parseMoment(field, raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, apperr.Invalidf("time_field_required", "%s 不能为空", field)
	}
	moment, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, apperr.Invalidf("time_field_invalid", "%s 必须是 RFC3339 时间", field)
	}
	return moment, nil
}

func splitQueryList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
