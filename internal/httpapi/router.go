package httpapi

import (
	"net/http"
	"time"

	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/middleware"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/auth"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
)

// Version is the API contract version reported by the version endpoint.
const Version = "v1"

// Router wires the HTTP surface onto the services.
type Router struct {
	auth      *auth.Service
	fleet     *fleet.Service
	dataloop  *dataloop.Service
	store     repository.Store
	logger    *logging.Logger
	clock     clock.Clock
	timeout   time.Duration
	startedAt time.Time
	sessions  *middleware.SessionCache
}

// SessionCacheWindow is how long a resolved bearer token stays memoised in the
// HTTP layer before the session store is consulted again.
const SessionCacheWindow = 30 * time.Second

// Dependencies bundles the collaborators of the HTTP layer.
type Dependencies struct {
	Auth           *auth.Service
	Fleet          *fleet.Service
	DataLoop       *dataloop.Service
	Store          repository.Store
	Logger         *logging.Logger
	Clock          clock.Clock
	RequestTimeout time.Duration
}

// NewRouter builds the router.
func NewRouter(dependencies Dependencies) *Router {
	source := dependencies.Clock
	if source == nil {
		source = clock.System{}
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = logging.New(nil, logging.LevelInfo)
	}
	return &Router{
		auth:      dependencies.Auth,
		fleet:     dependencies.Fleet,
		dataloop:  dependencies.DataLoop,
		store:     dependencies.Store,
		logger:    logger,
		clock:     source,
		timeout:   dependencies.RequestTimeout,
		startedAt: source.Now(),
		sessions:  middleware.NewSessionCache(SessionCacheWindow),
	}
}

// Handler returns the fully wired HTTP handler.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", rt.handleLive)
	mux.HandleFunc("GET /readyz", rt.handleReady)
	mux.HandleFunc("GET /api/v1/version", rt.handleVersion)
	mux.HandleFunc("POST /api/v1/auth/login", rt.handleLogin)

	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/v1/auth/logout", rt.handleLogout)
	protected.HandleFunc("GET /api/v1/auth/me", rt.handleWhoAmI)
	protected.HandleFunc("POST /api/v1/operators", rt.handleRegisterOperator)
	protected.HandleFunc("POST /api/v1/operators/{id}/session-revocations", rt.handleRevokeSessions)

	protected.HandleFunc("POST /api/v1/campaigns", rt.handleCreateCampaign)
	protected.HandleFunc("GET /api/v1/campaigns", rt.handleListCampaigns)
	protected.HandleFunc("GET /api/v1/campaigns/{id}", rt.handleGetCampaign)
	protected.HandleFunc("POST /api/v1/campaigns/{id}/transitions", rt.handleTransitionCampaign)
	protected.HandleFunc("GET /api/v1/campaigns/{id}/settlement-summary", rt.handleCampaignSettlements)

	protected.HandleFunc("POST /api/v1/vehicles", rt.handleRegisterVehicle)
	protected.HandleFunc("GET /api/v1/vehicles", rt.handleListVehicles)

	protected.HandleFunc("POST /api/v1/assignments", rt.handleCreateAssignment)
	protected.HandleFunc("GET /api/v1/assignments", rt.handleListAssignments)
	protected.HandleFunc("GET /api/v1/assignments/{id}", rt.handleGetAssignment)
	protected.HandleFunc("POST /api/v1/assignments/{id}/abort", rt.handleAbortAssignment)
	protected.HandleFunc("POST /api/v1/assignments/batch-abort", rt.handleBatchAbortAssignments)
	protected.HandleFunc("POST /api/v1/assignments/{id}/drives", rt.handleStartDrive)
	protected.HandleFunc("POST /api/v1/assignments/{id}/settlement", rt.handleSettleAssignment)

	protected.HandleFunc("GET /api/v1/drives/{id}", rt.handleGetDrive)
	protected.HandleFunc("POST /api/v1/drives/{id}/mileage", rt.handleReportMileage)
	protected.HandleFunc("POST /api/v1/drives/{id}/takeovers", rt.handleReportTakeover)
	protected.HandleFunc("POST /api/v1/drives/{id}/close", rt.handleCloseDrive)
	protected.HandleFunc("POST /api/v1/takeovers/{id}/resolve", rt.handleResolveTakeover)

	protected.HandleFunc("GET /api/v1/settlements/{id}", rt.handleGetSettlement)
	protected.HandleFunc("POST /api/v1/settlements/{id}/approve", rt.handleApproveSettlement)

	protected.HandleFunc("POST /api/v1/captures", rt.handleUploadCapture)
	protected.HandleFunc("GET /api/v1/captures", rt.handleListCaptures)
	protected.HandleFunc("GET /api/v1/captures/{id}", rt.handleGetCapture)
	protected.HandleFunc("POST /api/v1/captures/{id}/validate", rt.handleValidateCapture)
	protected.HandleFunc("POST /api/v1/captures/{id}/reject", rt.handleRejectCapture)

	protected.HandleFunc("GET /api/v1/triage-tickets", rt.handleListTickets)
	protected.HandleFunc("POST /api/v1/triage-tickets/{id}/assignee", rt.handleAssignTicket)
	protected.HandleFunc("POST /api/v1/triage-tickets/{id}/investigate", rt.handleInvestigateTicket)
	protected.HandleFunc("POST /api/v1/triage-tickets/{id}/disposition", rt.handleDisposeTicket)

	protected.HandleFunc("POST /api/v1/datasets", rt.handleCreateDataset)
	protected.HandleFunc("GET /api/v1/datasets/{id}", rt.handleGetDataset)
	protected.HandleFunc("POST /api/v1/datasets/{id}/frames", rt.handleAddDatasetFrames)
	protected.HandleFunc("DELETE /api/v1/datasets/{id}/frames/{frame_id}", rt.handleRemoveDatasetFrame)
	protected.HandleFunc("POST /api/v1/datasets/{id}/seal", rt.handleSealDataset)
	protected.HandleFunc("POST /api/v1/datasets/{id}/release", rt.handleReleaseDataset)

	mux.Handle("/api/v1/", middleware.Authenticate(rt.auth, rt.sessions, WriteError)(protected))

	return middleware.Chain(mux,
		middleware.RequestID,
		middleware.Recover(rt.logger, WriteError),
		middleware.AccessLog(rt.logger),
		middleware.Timeout(rt.timeout),
	)
}
