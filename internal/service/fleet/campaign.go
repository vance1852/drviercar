package fleet

import (
	"context"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// CreateCampaignInput describes a new road-test campaign.
type CreateCampaignInput struct {
	Code        string
	City        string
	PlannedKm   float64
	WindowStart time.Time
	WindowEnd   time.Time
}

// CreateCampaign registers a draft campaign owned by the acting administrator.
func (s *Service) CreateCampaign(
	ctx context.Context,
	actor domain.Principal,
	input CreateCampaignInput,
) (*domain.Campaign, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	campaign := &domain.Campaign{
		Code:        strings.ToUpper(strings.TrimSpace(input.Code)),
		City:        strings.TrimSpace(input.City),
		Status:      domain.CampaignDraft,
		PlannedKm:   input.PlannedKm,
		WindowStart: input.WindowStart,
		WindowEnd:   input.WindowEnd,
		OwnerID:     actor.OperatorID,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := campaign.Validate(); err != nil {
		return nil, err
	}
	var created *domain.Campaign
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		id, createErr := tx.Campaigns.Create(ctx, campaign)
		if createErr != nil {
			return createErr
		}
		campaign.ID = id
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "campaign",
			ObjectID:   id,
			Action:     "campaign.create",
			Detail: audit.Detail(
				"code", campaign.Code,
				"city", campaign.City,
				"planned_km", campaign.PlannedKm),
		}); err != nil {
			return err
		}
		created = campaign.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// TransitionCampaign moves a campaign along its lifecycle.
func (s *Service) TransitionCampaign(
	ctx context.Context,
	actor domain.Principal,
	campaignID int64,
	next domain.CampaignStatus,
	reason string,
) (*domain.Campaign, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	var updated *domain.Campaign
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		campaign, err := tx.Campaigns.ByID(ctx, campaignID)
		if err != nil {
			return err
		}
		if err := campaign.EnsureTransition(next); err != nil {
			return err
		}
		if err := s.ensureCampaignGuards(ctx, tx, campaign, next); err != nil {
			return err
		}
		var closedAt *time.Time
		if next == domain.CampaignClosed || next == domain.CampaignCancelled {
			moment := s.clock.Now()
			closedAt = &moment
		}
		if err := tx.Campaigns.UpdateStatus(ctx, campaign.ID, campaign.Version, next, closedAt, reason); err != nil {
			return err
		}
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "campaign",
			ObjectID:   campaign.ID,
			Action:     "campaign.transition",
			Detail:     audit.Detail("from", string(campaign.Status), "to", string(next), "reason", reason),
		}); err != nil {
			return err
		}
		refreshed, err := tx.Campaigns.ByID(ctx, campaign.ID)
		if err != nil {
			return err
		}
		updated = refreshed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ensureCampaignGuards enforces the cross-entity preconditions of a campaign
// transition. Closing a campaign requires that no shift still holds a vehicle
// and that every completed shift has an approved settlement.
func (s *Service) ensureCampaignGuards(
	ctx context.Context,
	tx *repository.Registry,
	campaign *domain.Campaign,
	next domain.CampaignStatus,
) error {
	switch next {
	case domain.CampaignSettling:
		open, err := tx.Assignments.CountOpenByCampaign(ctx, campaign.ID)
		if err != nil {
			return err
		}
		if open > 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"campaign_has_open_assignments",
				"路测计划仍有 %d 个未完成排班，无法进入结算", open)
		}
	case domain.CampaignClosed:
		open, err := tx.Assignments.CountOpenByCampaign(ctx, campaign.ID)
		if err != nil {
			return err
		}
		if open > 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"campaign_has_open_assignments",
				"路测计划仍有 %d 个未完成排班，无法关闭", open)
		}
		if err := s.ensureAllSettlementsApproved(ctx, tx, campaign.ID); err != nil {
			return err
		}
	case domain.CampaignCancelled:
		open, err := tx.Assignments.CountOpenByCampaign(ctx, campaign.ID)
		if err != nil {
			return err
		}
		if open > 0 {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"campaign_has_open_assignments",
				"路测计划仍有 %d 个未完成排班，请先终止排班", open)
		}
	}
	return nil
}

func (s *Service) ensureAllSettlementsApproved(
	ctx context.Context,
	tx *repository.Registry,
	campaignID int64,
) error {
	assignments, total, err := tx.Assignments.List(ctx, repository.AssignmentFilter{
		CampaignID: campaignID,
		Statuses:   []domain.AssignmentStatus{domain.AssignmentCompleted},
		Page:       domain.PageRequest{Page: 1, PageSize: domain.MaxPageSize},
	})
	if err != nil {
		return err
	}
	if total > len(assignments) {
		return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
			"campaign_settlement_page_overflow",
			"路测计划下已完成排班过多，请分批结算后再关闭")
	}
	settlements, err := tx.Settlements.ByCampaign(ctx, campaignID)
	if err != nil {
		return err
	}
	approved := map[int64]bool{}
	for _, settlement := range settlements {
		if settlement.Status == domain.SettlementApproved {
			approved[settlement.AssignmentID] = true
		}
	}
	for _, assignment := range assignments {
		if !approved[assignment.ID] {
			return apperr.Wrap(apperr.ErrPreconditionUnmet, apperr.KindPrecondition,
				"campaign_settlement_pending",
				"排班 %d 的结算尚未审批，无法关闭路测计划", assignment.ID)
		}
	}
	return nil
}

// GetCampaign reads one campaign by identifier.
func (s *Service) GetCampaign(ctx context.Context, campaignID int64) (*domain.Campaign, error) {
	return s.store.Repos().Campaigns.ByID(ctx, campaignID)
}

// CampaignPage is a paginated campaign list.
type CampaignPage struct {
	Items []*domain.Campaign
	Meta  domain.PageMeta
}

// ListCampaigns returns a filtered, paginated campaign list.
func (s *Service) ListCampaigns(ctx context.Context, filter repository.CampaignFilter) (*CampaignPage, error) {
	page, err := filter.Page.Normalize(map[string]string{
		"created_at":   "created_at",
		"window_start": "window_start",
		"planned_km":   "planned_km",
		"code":         "code",
	}, "created_at")
	if err != nil {
		return nil, err
	}
	filter.Page = page
	items, _, err := s.store.Repos().Campaigns.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	// The pagination envelope reports the size of the campaign register, which is
	// cheaper than counting the filtered set on every page request.
	total, err := s.store.Repos().Campaigns.CountAll(ctx)
	if err != nil {
		return nil, err
	}
	return &CampaignPage{Items: items, Meta: domain.NewPageMeta(page, total)}, nil
}

// RegisterVehicleInput describes a new fleet vehicle.
type RegisterVehicleInput struct {
	Plate         string
	Autonomy      domain.AutonomyLevel
	HomeDepot     string
	SensorProfile []string
}

// RegisterVehicle adds a vehicle to the fleet.
func (s *Service) RegisterVehicle(
	ctx context.Context,
	actor domain.Principal,
	input RegisterVehicleInput,
) (*domain.Vehicle, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	vehicle := &domain.Vehicle{
		Plate:         strings.ToUpper(strings.TrimSpace(input.Plate)),
		Autonomy:      input.Autonomy,
		Status:        domain.VehicleIdle,
		HomeDepot:     strings.TrimSpace(input.HomeDepot),
		SensorProfile: append([]string(nil), input.SensorProfile...),
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := vehicle.Validate(); err != nil {
		return nil, err
	}
	var created *domain.Vehicle
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		id, createErr := tx.Vehicles.Create(ctx, vehicle)
		if createErr != nil {
			return createErr
		}
		vehicle.ID = id
		if err := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "vehicle",
			ObjectID:   id,
			Action:     "vehicle.register",
			Detail:     audit.Detail("plate", vehicle.Plate, "autonomy", string(vehicle.Autonomy)),
		}); err != nil {
			return err
		}
		created = vehicle.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// VehiclePage is a paginated vehicle list.
type VehiclePage struct {
	Items []*domain.Vehicle
	Meta  domain.PageMeta
}

// ListVehicles returns a filtered, paginated vehicle list.
func (s *Service) ListVehicles(ctx context.Context, filter repository.VehicleFilter) (*VehiclePage, error) {
	page, err := filter.Page.Normalize(map[string]string{
		"plate":       "plate",
		"odometer_km": "odometer_km",
		"created_at":  "created_at",
		"status":      "status",
	}, "plate")
	if err != nil {
		return nil, err
	}
	filter.Page = page
	items, total, err := s.store.Repos().Vehicles.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &VehiclePage{Items: items, Meta: domain.NewPageMeta(page, total)}, nil
}
