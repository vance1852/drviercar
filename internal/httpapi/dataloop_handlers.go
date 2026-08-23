package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/service/dataloop"
)

type frameRequest struct {
	Sequence     int     `json:"sequence"`
	Sensor       string  `json:"sensor"`
	PayloadHash  string  `json:"payload_hash"`
	QualityScore float64 `json:"quality_score"`
	CapturedAt   string  `json:"captured_at"`
}

type uploadRequest struct {
	DriveID   int64          `json:"drive_id"`
	UploadKey string         `json:"upload_key"`
	Manifest  string         `json:"manifest"`
	Frames    []frameRequest `json:"frames"`
}

func (rt *Router) handleUploadCapture(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request uploadRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	frames := make([]dataloop.FrameInput, 0, len(request.Frames))
	for index, frame := range request.Frames {
		captured := time.Time{}
		if strings.TrimSpace(frame.CapturedAt) != "" {
			parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(frame.CapturedAt))
			if parseErr != nil {
				WriteError(w, r, apperr.Invalidf("frame_captured_at_invalid",
					"第 %d 帧的采集时间必须是 RFC3339 时间", index+1))
				return
			}
			captured = parsed
		}
		frames = append(frames, dataloop.FrameInput{
			Sequence:     frame.Sequence,
			Sensor:       frame.Sensor,
			PayloadHash:  frame.PayloadHash,
			QualityScore: frame.QualityScore,
			CapturedAt:   captured,
		})
	}
	batch, err := rt.dataloop.UploadBatch(r.Context(), principal, dataloop.UploadInput{
		DriveID:   request.DriveID,
		UploadKey: request.UploadKey,
		Manifest:  request.Manifest,
		Frames:    frames,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newBatchView(batch))
}

func (rt *Router) handleValidateCapture(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	batchID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	outcome, err := rt.dataloop.ValidateBatch(r.Context(), principal, batchID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"batch":       newBatchView(outcome.Batch),
		"accepted":    outcome.Accepted,
		"quarantined": outcome.Quarantined,
		"ticket_ids":  outcome.TicketIDs,
	})
}

func (rt *Router) handleRejectCapture(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	batchID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request reasonRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	batch, err := rt.dataloop.RejectBatch(r.Context(), principal, batchID, request.Reason)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newBatchView(batch))
}

func (rt *Router) handleListCaptures(w http.ResponseWriter, r *http.Request) {
	page, err := PageFromQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	driveID, err := QueryInt(r, "drive_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	vehicleID, err := QueryInt(r, "vehicle_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.CaptureFilter{
		DriveID:   int64(driveID),
		VehicleID: int64(vehicleID),
		Page:      page,
	}
	for _, status := range splitQueryList(r.URL.Query().Get("status")) {
		filter.Statuses = append(filter.Statuses, domain.BatchStatus(status))
	}
	result, err := rt.dataloop.ListBatches(r.Context(), filter)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListEnvelope{Items: newBatchViews(result.Items), Meta: result.Meta})
}

func (rt *Router) handleGetCapture(w http.ResponseWriter, r *http.Request) {
	batchID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	detail, err := rt.dataloop.DescribeBatch(r.Context(), batchID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"batch":  newBatchView(detail.Batch),
		"frames": newFrameViews(detail.Frames),
	})
}

func (rt *Router) handleListTickets(w http.ResponseWriter, r *http.Request) {
	page, err := PageFromQuery(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	batchID, err := QueryInt(r, "batch_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	assigneeID, err := QueryInt(r, "assignee_id", 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.TicketFilter{
		BatchID:    int64(batchID),
		AssigneeID: int64(assigneeID),
		Page:       page,
	}
	for _, status := range splitQueryList(r.URL.Query().Get("status")) {
		filter.Statuses = append(filter.Statuses, domain.TicketStatus(status))
	}
	result, err := rt.dataloop.ListTickets(r.Context(), filter)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListEnvelope{Items: newTicketViews(result.Items), Meta: result.Meta})
}

type assignTicketRequest struct {
	AssigneeID int64 `json:"assignee_id"`
}

func (rt *Router) handleAssignTicket(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request assignTicketRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	ticket, err := rt.dataloop.AssignTicket(r.Context(), principal, ticketID, request.AssigneeID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newTicketView(ticket))
}

func (rt *Router) handleInvestigateTicket(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticket, err := rt.dataloop.StartInvestigation(r.Context(), principal, ticketID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newTicketView(ticket))
}

type disposeRequest struct {
	Disposition string `json:"disposition"`
	Conclusion  string `json:"conclusion"`
}

func (rt *Router) handleDisposeTicket(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request disposeRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	ticket, err := rt.dataloop.DisposeTicket(r.Context(), principal, dataloop.DisposeInput{
		TicketID:    ticketID,
		Disposition: domain.Disposition(strings.TrimSpace(request.Disposition)),
		Conclusion:  request.Conclusion,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newTicketView(ticket))
}

type createDatasetRequest struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
}

func (rt *Router) handleCreateDataset(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request createDatasetRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	dataset, err := rt.dataloop.CreateDataset(r.Context(), principal, dataloop.CreateDatasetInput{
		Name:    request.Name,
		Purpose: request.Purpose,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, newDatasetView(dataset))
}

type datasetFramesRequest struct {
	FrameIDs []int64 `json:"frame_ids"`
}

func (rt *Router) handleAddDatasetFrames(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	datasetID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var request datasetFramesRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	outcome, err := rt.dataloop.AddFrames(r.Context(), principal, datasetID, request.FrameIDs)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, outcome)
}

func (rt *Router) handleRemoveDatasetFrame(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	datasetID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	frameID, err := PathInt64(r, "frame_id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := rt.dataloop.RemoveFrame(r.Context(), principal, datasetID, frameID); err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"removed": true})
}

func (rt *Router) handleSealDataset(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	datasetID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	dataset, err := rt.dataloop.SealDataset(r.Context(), principal, datasetID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newDatasetView(dataset))
}

func (rt *Router) handleReleaseDataset(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	datasetID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	dataset, err := rt.dataloop.ReleaseDataset(r.Context(), principal, datasetID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newDatasetView(dataset))
}

func (rt *Router) handleGetDataset(w http.ResponseWriter, r *http.Request) {
	datasetID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	dataset, err := rt.dataloop.GetDataset(r.Context(), datasetID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	members, err := rt.dataloop.DatasetMembers(r.Context(), datasetID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"dataset":   newDatasetView(dataset),
		"frame_ids": members,
	})
}
