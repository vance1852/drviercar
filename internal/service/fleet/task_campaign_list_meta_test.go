package fleet_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/fleet"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestCampaignListMetaMatchesRows pages through the campaign register with a
// status filter and checks that the pagination envelope describes the filtered
// result instead of the whole register.
func TestCampaignListMetaMatchesRows(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()
	ctx := context.Background()

	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}

	const (
		running = 3
		draft   = 4
	)
	for index := 0; index < running; index++ {
		campaign, createErr := harness.Fleet.CreateCampaign(ctx, actors.Admin, fleet.CreateCampaignInput{
			Code:        fmt.Sprintf("PAGE-RUN-%02d", index),
			City:        "shanghai-jiading",
			PlannedKm:   200,
			WindowStart: testsupport.Anchor,
			WindowEnd:   testsupport.Anchor.Add(48 * time.Hour),
		})
		if createErr != nil {
			t.Fatalf("create running campaign %d: %v", index, createErr)
		}
		if _, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
			domain.CampaignScheduled, "ready"); err != nil {
			t.Fatalf("schedule campaign %d: %v", index, err)
		}
		if _, err := harness.Fleet.TransitionCampaign(ctx, actors.Admin, campaign.ID,
			domain.CampaignRunning, "fleet on road"); err != nil {
			t.Fatalf("start campaign %d: %v", index, err)
		}
	}
	for index := 0; index < draft; index++ {
		if _, createErr := harness.Fleet.CreateCampaign(ctx, actors.Admin, fleet.CreateCampaignInput{
			Code:        fmt.Sprintf("PAGE-DRAFT-%02d", index),
			City:        "shanghai-jiading",
			PlannedKm:   150,
			WindowStart: testsupport.Anchor,
			WindowEnd:   testsupport.Anchor.Add(48 * time.Hour),
		}); createErr != nil {
			t.Fatalf("create draft campaign %d: %v", index, createErr)
		}
	}

	first, err := harness.Fleet.ListCampaigns(ctx, repository.CampaignFilter{
		Statuses: []domain.CampaignStatus{domain.CampaignRunning},
		Page: domain.PageRequest{
			Page: 1, PageSize: 2, SortField: "code", SortDir: domain.SortAsc,
		},
	})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if first.Meta.Total != running {
		t.Fatalf("the envelope must count only the filtered campaigns: total=%d, want %d",
			first.Meta.Total, running)
	}
	if first.Meta.TotalPages != 2 {
		t.Fatalf("three filtered rows at page size two make two pages, got %d", first.Meta.TotalPages)
	}
	if !first.Meta.HasNext {
		t.Fatal("the first of two pages must report a next page")
	}
	if len(first.Items) != 2 {
		t.Fatalf("the first page must return two rows, got %d", len(first.Items))
	}
	for _, item := range first.Items {
		if item.Status != domain.CampaignRunning {
			t.Fatalf("the filter must only return running campaigns, got %s", item.Status)
		}
	}

	second, err := harness.Fleet.ListCampaigns(ctx, repository.CampaignFilter{
		Statuses: []domain.CampaignStatus{domain.CampaignRunning},
		Page: domain.PageRequest{
			Page: 2, PageSize: 2, SortField: "code", SortDir: domain.SortAsc,
		},
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("the last page must return the remaining row, got %d", len(second.Items))
	}
	if second.Meta.HasNext {
		t.Fatal("the last page must not report a next page")
	}
	if second.Meta.Total != running {
		t.Fatalf("the envelope total must stay %d across pages, got %d", running, second.Meta.Total)
	}

	beyond, err := harness.Fleet.ListCampaigns(ctx, repository.CampaignFilter{
		Statuses: []domain.CampaignStatus{domain.CampaignRunning},
		Page: domain.PageRequest{
			Page: 3, PageSize: 2, SortField: "code", SortDir: domain.SortAsc,
		},
	})
	if err != nil {
		t.Fatalf("list page beyond the result: %v", err)
	}
	if len(beyond.Items) != 0 {
		t.Fatalf("no rows exist beyond the filtered result, got %d", len(beyond.Items))
	}
	if beyond.Meta.HasNext {
		t.Fatal("a page beyond the filtered result must not report a next page")
	}

	unfiltered, err := harness.Fleet.ListCampaigns(ctx, repository.CampaignFilter{
		Page: domain.PageRequest{Page: 1, PageSize: 100, SortField: "code", SortDir: domain.SortAsc},
	})
	if err != nil {
		t.Fatalf("list without a filter: %v", err)
	}
	if unfiltered.Meta.Total != running+draft {
		t.Fatalf("without a filter the envelope must count every campaign: total=%d, want %d",
			unfiltered.Meta.Total, running+draft)
	}
}
