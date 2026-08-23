package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Operator  struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	} `json:"operator"`
}

func (rt *Router) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	result, err := rt.auth.Login(r.Context(), auth.Credentials{
		Username: request.Username,
		Password: request.Password,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response := loginResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt.Format(time.RFC3339),
	}
	response.Operator.ID = result.Operator.ID
	response.Operator.Username = result.Operator.Username
	response.Operator.DisplayName = result.Operator.DisplayName
	response.Operator.Role = string(result.Operator.Role)
	WriteJSON(w, http.StatusOK, response)
}

func (rt *Router) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if err := rt.auth.Logout(r.Context(), token); err != nil {
		WriteError(w, r, err)
		return
	}
	rt.sessions.Forget(token)
	WriteJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

type registerRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

func (rt *Router) handleRegisterOperator(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := principal.RequireRole(domain.RoleFleetAdmin); err != nil {
		WriteError(w, r, err)
		return
	}
	var request registerRequest
	if err := DecodeJSON(r, &request); err != nil {
		WriteError(w, r, err)
		return
	}
	operator, err := rt.auth.Register(r.Context(), auth.RegisterInput{
		Username:    request.Username,
		DisplayName: request.DisplayName,
		Password:    request.Password,
		Role:        domain.Role(request.Role),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id":           operator.ID,
		"username":     operator.Username,
		"display_name": operator.DisplayName,
		"role":         string(operator.Role),
	})
}

func (rt *Router) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"operator_id": principal.OperatorID,
		"username":    principal.Username,
		"role":        string(principal.Role),
		"session_id":  principal.SessionID,
		"expires_at":  principal.ExpiresAt.Format(time.RFC3339),
	})
}

func (rt *Router) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	principal, err := Principal(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	operatorID, err := PathInt64(r, "id")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	revoked, err := rt.auth.RevokeOperatorSessions(r.Context(), principal, operatorID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"revoked": revoked})
}
