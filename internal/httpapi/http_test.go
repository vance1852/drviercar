package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/httpapi"
	"github.com/vance1852/drviercar/internal/middleware"
	"github.com/vance1852/drviercar/internal/testsupport"
)

type client struct {
	t       *testing.T
	server  *httptest.Server
	harness *testsupport.Harness
	actors  *testsupport.Actors
}

func newClient(t *testing.T) *client {
	t.Helper()
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	actors, err := harness.SeedActors(context.Background())
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	router := httpapi.NewRouter(httpapi.Dependencies{
		Auth:           harness.Auth,
		Fleet:          harness.Fleet,
		DataLoop:       harness.DataLoop,
		Store:          harness.Store,
		Clock:          harness.Clock,
		RequestTimeout: 5 * time.Second,
	})
	server := httptest.NewServer(router.Handler())
	t.Cleanup(func() {
		server.Close()
		_ = harness.Close()
	})
	return &client{t: t, server: server, harness: harness, actors: actors}
}

type response struct {
	status  int
	body    map[string]any
	headers http.Header
}

func (c *client) do(method, path, token string, payload any, headers map[string]string) response {
	c.t.Helper()
	var reader *bytes.Reader
	if payload == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("encode payload: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	result, err := c.server.Client().Do(request)
	if err != nil {
		c.t.Fatalf("perform request: %v", err)
	}
	defer result.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(result.Body).Decode(&decoded)
	return response{status: result.StatusCode, body: decoded, headers: result.Header}
}

func TestHealthAndVersionEndpointsAreOpen(t *testing.T) {
	c := newClient(t)
	live := c.do(http.MethodGet, "/healthz", "", nil, nil)
	if live.status != http.StatusOK || live.body["status"] != "alive" {
		t.Fatalf("unexpected liveness response %+v", live)
	}
	ready := c.do(http.MethodGet, "/readyz", "", nil, nil)
	if ready.status != http.StatusOK || ready.body["database"] != "ok" {
		t.Fatalf("unexpected readiness response %+v", ready)
	}
	version := c.do(http.MethodGet, "/api/v1/version", "", nil, nil)
	if version.status != http.StatusOK || version.body["api_version"] != "v1" {
		t.Fatalf("unexpected version response %+v", version)
	}
	if version.headers.Get(middleware.RequestIDHeader) == "" {
		t.Fatal("every response must carry a request id")
	}
}

func TestRequestIDIsEchoedBack(t *testing.T) {
	c := newClient(t)
	result := c.do(http.MethodGet, "/healthz", "", nil, map[string]string{
		middleware.RequestIDHeader: "req-fixed-value",
	})
	if got := result.headers.Get(middleware.RequestIDHeader); got != "req-fixed-value" {
		t.Fatalf("the supplied request id must be reused, got %q", got)
	}
}

func TestProtectedRoutesRequireBearerToken(t *testing.T) {
	c := newClient(t)
	anonymous := c.do(http.MethodGet, "/api/v1/auth/me", "", nil, nil)
	if anonymous.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", anonymous.status)
	}
	errorBody, ok := anonymous.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("the error envelope is missing: %+v", anonymous.body)
	}
	if errorBody["code"] != "session_token_missing" {
		t.Fatalf("unexpected error code %v", errorBody["code"])
	}
	if errorBody["request_id"] == "" {
		t.Fatal("the error envelope must carry the request id")
	}
	garbage := c.do(http.MethodGet, "/api/v1/auth/me", "not-a-real-token", nil, nil)
	if garbage.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 with an unknown token, got %d", garbage.status)
	}
}

func TestLoginLogoutRoundTrip(t *testing.T) {
	c := newClient(t)
	login := c.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "fleet-admin",
		"password": "admin-secret-1",
	}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("login failed: %+v", login)
	}
	token, ok := login.body["token"].(string)
	if !ok || token == "" {
		t.Fatalf("the login response must contain a token: %+v", login.body)
	}
	me := c.do(http.MethodGet, "/api/v1/auth/me", token, nil, nil)
	if me.status != http.StatusOK || me.body["role"] != string(domain.RoleFleetAdmin) {
		t.Fatalf("unexpected whoami response %+v", me)
	}
	logout := c.do(http.MethodPost, "/api/v1/auth/logout", token, nil, nil)
	if logout.status != http.StatusOK {
		t.Fatalf("logout failed: %+v", logout)
	}
	after := c.do(http.MethodGet, "/api/v1/auth/me", token, nil, nil)
	if after.status != http.StatusUnauthorized {
		t.Fatalf("a revoked token must stop working, got %d", after.status)
	}
	wrong := c.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": "fleet-admin",
		"password": "not-the-password",
	}, nil)
	if wrong.status != http.StatusUnauthorized {
		t.Fatalf("bad credentials must return 401, got %d", wrong.status)
	}
}

func TestCampaignEndpointsEnforceRoleAndValidation(t *testing.T) {
	c := newClient(t)
	forbidden := c.do(http.MethodPost, "/api/v1/campaigns", c.actors.OperatorToken, map[string]any{
		"code":         "HT-1",
		"city":         "shanghai",
		"planned_km":   200,
		"window_start": testsupport.Anchor.Format(time.RFC3339),
		"window_end":   testsupport.Anchor.Add(24 * time.Hour).Format(time.RFC3339),
	}, nil)
	if forbidden.status != http.StatusForbidden {
		t.Fatalf("a safety operator must not create campaigns, got %d", forbidden.status)
	}

	created := c.do(http.MethodPost, "/api/v1/campaigns", c.actors.AdminToken, map[string]any{
		"code":         "HT-1",
		"city":         "shanghai",
		"planned_km":   200,
		"window_start": testsupport.Anchor.Format(time.RFC3339),
		"window_end":   testsupport.Anchor.Add(24 * time.Hour).Format(time.RFC3339),
	}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("campaign creation failed: %+v", created)
	}
	if created.body["status"] != string(domain.CampaignDraft) {
		t.Fatalf("a new campaign must be a draft, got %v", created.body["status"])
	}

	badWindow := c.do(http.MethodPost, "/api/v1/campaigns", c.actors.AdminToken, map[string]any{
		"code":         "HT-2",
		"city":         "shanghai",
		"planned_km":   200,
		"window_start": "not-a-timestamp",
		"window_end":   testsupport.Anchor.Add(24 * time.Hour).Format(time.RFC3339),
	}, nil)
	if badWindow.status != http.StatusBadRequest {
		t.Fatalf("an invalid timestamp must return 400, got %d", badWindow.status)
	}

	unknownField := c.do(http.MethodPost, "/api/v1/campaigns", c.actors.AdminToken, map[string]any{
		"code":          "HT-3",
		"city":          "shanghai",
		"planned_km":    200,
		"window_start":  testsupport.Anchor.Format(time.RFC3339),
		"window_end":    testsupport.Anchor.Add(24 * time.Hour).Format(time.RFC3339),
		"secret_option": true,
	}, nil)
	if unknownField.status != http.StatusBadRequest {
		t.Fatalf("an unknown field must be rejected, got %d", unknownField.status)
	}

	duplicate := c.do(http.MethodPost, "/api/v1/campaigns", c.actors.AdminToken, map[string]any{
		"code":         "HT-1",
		"city":         "shanghai",
		"planned_km":   200,
		"window_start": testsupport.Anchor.Format(time.RFC3339),
		"window_end":   testsupport.Anchor.Add(24 * time.Hour).Format(time.RFC3339),
	}, nil)
	if duplicate.status != http.StatusConflict {
		t.Fatalf("a duplicate campaign code must return 409, got %d", duplicate.status)
	}

	missing := c.do(http.MethodGet, "/api/v1/campaigns/999999", c.actors.AdminToken, nil, nil)
	if missing.status != http.StatusNotFound {
		t.Fatalf("a missing campaign must return 404, got %d", missing.status)
	}
	badPath := c.do(http.MethodGet, "/api/v1/campaigns/abc", c.actors.AdminToken, nil, nil)
	if badPath.status != http.StatusBadRequest {
		t.Fatalf("a non numeric path parameter must return 400, got %d", badPath.status)
	}
}

func TestAssignmentEndpointRequiresIdempotencyKey(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	campaign, err := c.harness.SeedCampaign(ctx, c.actors.Admin, "HT-10", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := c.harness.SeedVehicle(ctx, c.actors.Admin, "沪AD60606", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	payload := map[string]any{
		"campaign_id": campaign.ID,
		"vehicle_id":  vehicle.ID,
		"operator_id": c.actors.Operator.OperatorID,
		"planned_km":  120,
		"shift_start": testsupport.Anchor.Format(time.RFC3339),
		"shift_end":   testsupport.Anchor.Add(5 * time.Hour).Format(time.RFC3339),
		"route":       "http-loop",
	}
	missing := c.do(http.MethodPost, "/api/v1/assignments", c.actors.AdminToken, payload, nil)
	if missing.status != http.StatusBadRequest {
		t.Fatalf("a missing idempotency key must return 400, got %d", missing.status)
	}
	created := c.do(http.MethodPost, "/api/v1/assignments", c.actors.AdminToken, payload,
		map[string]string{"Idempotency-Key": "http-idem-1"})
	if created.status != http.StatusCreated {
		t.Fatalf("assignment creation failed: %+v", created)
	}
	replay := c.do(http.MethodPost, "/api/v1/assignments", c.actors.AdminToken, payload,
		map[string]string{"Idempotency-Key": "http-idem-1"})
	if replay.status != http.StatusCreated {
		t.Fatalf("replaying the same key must succeed, got %d", replay.status)
	}
	if created.body["id"] != replay.body["id"] {
		t.Fatalf("a replay must return the same assignment, got %v and %v", created.body["id"], replay.body["id"])
	}
	list := c.do(http.MethodGet, "/api/v1/assignments?status=planned&page=1&page_size=5",
		c.actors.AdminToken, nil, nil)
	if list.status != http.StatusOK {
		t.Fatalf("listing assignments failed: %+v", list)
	}
	meta, ok := list.body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("the list envelope must carry meta: %+v", list.body)
	}
	if meta["total"].(float64) != 1 {
		t.Fatalf("one assignment expected, meta says %v", meta["total"])
	}
	badSort := c.do(http.MethodGet, "/api/v1/assignments?sort_by=route", c.actors.AdminToken, nil, nil)
	if badSort.status != http.StatusBadRequest {
		t.Fatalf("an unlisted sort column must return 400, got %d", badSort.status)
	}
}

func TestDriveAndCaptureEndToEndOverHTTP(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	campaign, err := c.harness.SeedCampaign(ctx, c.actors.Admin, "HT-20", 400)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	vehicle, err := c.harness.SeedVehicle(ctx, c.actors.Admin, "沪AD61616", domain.AutonomyL4)
	if err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}
	assignment := c.do(http.MethodPost, "/api/v1/assignments", c.actors.AdminToken, map[string]any{
		"campaign_id": campaign.ID,
		"vehicle_id":  vehicle.ID,
		"operator_id": c.actors.Operator.OperatorID,
		"planned_km":  150,
		"shift_start": testsupport.Anchor.Format(time.RFC3339),
		"shift_end":   testsupport.Anchor.Add(6 * time.Hour).Format(time.RFC3339),
		"route":       "http-drive-loop",
	}, map[string]string{"Idempotency-Key": "http-idem-2"})
	if assignment.status != http.StatusCreated {
		t.Fatalf("assignment creation failed: %+v", assignment)
	}
	assignmentID := int64(assignment.body["id"].(float64))

	drive := c.do(http.MethodPost,
		"/api/v1/assignments/"+itoa(assignmentID)+"/drives", c.actors.OperatorToken, nil, nil)
	if drive.status != http.StatusCreated {
		t.Fatalf("starting the drive failed: %+v", drive)
	}
	driveID := int64(drive.body["id"].(float64))

	mileage := c.do(http.MethodPost, "/api/v1/drives/"+itoa(driveID)+"/mileage",
		c.actors.OperatorToken, map[string]any{"auto_km": 80, "manual_km": 2}, nil)
	if mileage.status != http.StatusOK {
		t.Fatalf("reporting mileage failed: %+v", mileage)
	}
	if mileage.body["total_km"].(float64) != 82 {
		t.Fatalf("unexpected total mileage %v", mileage.body["total_km"])
	}
	overPlan := c.do(http.MethodPost, "/api/v1/drives/"+itoa(driveID)+"/mileage",
		c.actors.OperatorToken, map[string]any{"auto_km": 200}, nil)
	if overPlan.status != http.StatusTooManyRequests {
		t.Fatalf("mileage beyond the plan must return 429, got %d", overPlan.status)
	}

	takeover := c.do(http.MethodPost, "/api/v1/drives/"+itoa(driveID)+"/takeovers",
		c.actors.OperatorToken, map[string]any{
			"category":    "control",
			"severity":    3,
			"manual_km":   1,
			"description": "steering hesitated at the merge",
		}, nil)
	if takeover.status != http.StatusCreated {
		t.Fatalf("reporting the takeover failed: %+v", takeover)
	}
	if takeover.body["critical"] != true {
		t.Fatalf("a control takeover must be critical, got %v", takeover.body["critical"])
	}

	frames := testsupport.BuildFrames([]testsupport.FrameSpec{
		{Sequence: 1, Sensor: "lidar-front", Quality: 0.94},
		{Sequence: 2, Sensor: "camera-ring", Quality: 0.31},
	}, testsupport.Anchor)
	frameBodies := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		frameBodies = append(frameBodies, map[string]any{
			"sequence":      frame.Sequence,
			"sensor":        frame.Sensor,
			"payload_hash":  frame.PayloadHash,
			"quality_score": frame.QualityScore,
			"captured_at":   frame.CapturedAt.Format(time.RFC3339),
		})
	}
	upload := c.do(http.MethodPost, "/api/v1/captures", c.actors.OperatorToken, map[string]any{
		"drive_id":   driveID,
		"upload_key": "http-upload-1",
		"manifest":   testsupport.Manifest(frames),
		"frames":     frameBodies,
	}, nil)
	if upload.status != http.StatusCreated {
		t.Fatalf("upload failed: %+v", upload)
	}
	batchID := int64(upload.body["id"].(float64))

	validate := c.do(http.MethodPost, "/api/v1/captures/"+itoa(batchID)+"/validate",
		c.actors.AdminToken, nil, nil)
	if validate.status != http.StatusOK {
		t.Fatalf("validation failed: %+v", validate)
	}
	if validate.body["accepted"].(float64) != 1 || validate.body["quarantined"].(float64) != 1 {
		t.Fatalf("unexpected validation counters %+v", validate.body)
	}
	tickets := validate.body["ticket_ids"].([]any)
	if len(tickets) != 1 {
		t.Fatalf("one triage ticket expected, got %d", len(tickets))
	}
	ticketID := int64(tickets[0].(float64))

	closeDrive := c.do(http.MethodPost, "/api/v1/drives/"+itoa(driveID)+"/close",
		c.actors.OperatorToken, nil, nil)
	if closeDrive.status != http.StatusOK {
		t.Fatalf("closing the drive failed: %+v", closeDrive)
	}

	blocked := c.do(http.MethodPost, "/api/v1/assignments/"+itoa(assignmentID)+"/settlement",
		c.actors.AdminToken, nil, nil)
	if blocked.status != http.StatusUnprocessableEntity {
		t.Fatalf("a pending triage ticket must block settlement with 422, got %d", blocked.status)
	}

	dispose := c.do(http.MethodPost, "/api/v1/triage-tickets/"+itoa(ticketID)+"/disposition",
		c.actors.AdminToken, map[string]any{
			"disposition": "software_bug",
			"conclusion":  "lane fusion dropped the right edge",
		}, nil)
	if dispose.status != http.StatusOK {
		t.Fatalf("disposing the ticket failed: %+v", dispose)
	}

	settlement := c.do(http.MethodPost, "/api/v1/assignments/"+itoa(assignmentID)+"/settlement",
		c.actors.AdminToken, nil, nil)
	if settlement.status != http.StatusCreated {
		t.Fatalf("settlement failed: %+v", settlement)
	}
	if settlement.body["critical_events"].(float64) != 1 {
		t.Fatalf("the unresolved control takeover must be counted, got %v", settlement.body["critical_events"])
	}
	if settlement.body["billable_km"].(float64) != 78.5 {
		t.Fatalf("unexpected billable mileage %v", settlement.body["billable_km"])
	}
	settlementID := int64(settlement.body["id"].(float64))
	approve := c.do(http.MethodPost, "/api/v1/settlements/"+itoa(settlementID)+"/approve",
		c.actors.AdminToken, map[string]any{"note": "reviewed with the control team"}, nil)
	if approve.status != http.StatusOK || approve.body["status"] != string(domain.SettlementApproved) {
		t.Fatalf("approving the settlement failed: %+v", approve)
	}
}

func TestUnknownRouteAndMethodMismatch(t *testing.T) {
	c := newClient(t)
	missing := c.do(http.MethodGet, "/api/v1/not-a-resource", c.actors.AdminToken, nil, nil)
	if missing.status != http.StatusNotFound {
		t.Fatalf("an unknown route must return 404, got %d", missing.status)
	}
	wrongMethod := c.do(http.MethodDelete, "/api/v1/campaigns", c.actors.AdminToken, nil, nil)
	if wrongMethod.status != http.StatusMethodNotAllowed && wrongMethod.status != http.StatusNotFound {
		t.Fatalf("an unsupported method must be refused, got %d", wrongMethod.status)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
