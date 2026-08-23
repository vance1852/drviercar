package httpapi

import (
	"net/http"
	"runtime"
	"time"
)

func (rt *Router) handleLive(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "alive",
		"uptime_sec": int64(rt.clock.Now().Sub(rt.startedAt).Seconds()),
	})
}

func (rt *Router) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := rt.store.Ping(r.Context()); err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "ready",
		"database": "ok",
	})
}

func (rt *Router) handleVersion(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"api_version": Version,
		"go_version":  runtime.Version(),
		"started_at":  rt.startedAt.Format(time.RFC3339),
	})
}

func (rt *Router) handleGetSettlement(w http.ResponseWriter, r *http.Request) {
	settlementID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	settlement, err := rt.fleet.GetSettlement(r.Context(), settlementID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, newSettlementView(settlement))
}
