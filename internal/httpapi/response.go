// Package httpapi exposes the platform over HTTP with a uniform JSON envelope.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/middleware"
)

// ErrorBody is the uniform error payload.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// ErrorEnvelope wraps ErrorBody.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// MaxRequestBytes bounds the accepted request payload.
const MaxRequestBytes = 1 << 20

func statusFor(err error) int {
	switch apperr.KindOf(err) {
	case apperr.KindInvalid:
		return http.StatusBadRequest
	case apperr.KindUnauthorized:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindPrecondition:
		return http.StatusUnprocessableEntity
	case apperr.KindExhausted:
		return http.StatusTooManyRequests
	case apperr.KindCancelled:
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

// WriteJSON renders a successful response.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// WriteError renders the uniform error envelope for err.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(err)
	body := ErrorEnvelope{Error: ErrorBody{
		Code:      apperr.CodeOf(err),
		Message:   apperr.MessageOf(err),
		RequestID: logging.RequestIDFrom(r.Context()),
	}}
	if status == http.StatusInternalServerError {
		body.Error.Message = "服务内部错误"
	}
	WriteJSON(w, status, body)
}

// DecodeJSON reads and validates a JSON request body.
func DecodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return apperr.Invalidf("request_body_required", "请求体不能为空")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, MaxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.Invalidf("request_body_required", "请求体不能为空")
		}
		return apperr.Wrap(err, apperr.KindInvalid, "request_body_invalid", "请求体不是合法的 JSON")
	}
	if decoder.More() {
		return apperr.Invalidf("request_body_trailing", "请求体只能包含一个 JSON 对象")
	}
	return nil
}

// Principal reads the authenticated principal or reports an authentication error.
func Principal(r *http.Request) (domain.Principal, error) {
	principal, ok := middleware.PrincipalFrom(r.Context())
	if !ok {
		return domain.Principal{}, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
			"session_required", "该接口需要登录")
	}
	return principal, nil
}

// PathInt64 reads a positive integer path parameter.
func PathInt64(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.PathValue(name))
	if raw == "" {
		return 0, apperr.Invalidf("path_param_required", "缺少路径参数 %s", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, apperr.Invalidf("path_param_invalid", "路径参数 %s 必须是正整数", name)
	}
	return value, nil
}

// QueryInt reads an optional integer query parameter.
func QueryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.Invalidf("query_param_invalid", "查询参数 %s 必须是整数", name)
	}
	return value, nil
}

// PageFromQuery builds a page request from the standard query parameters.
func PageFromQuery(r *http.Request) (domain.PageRequest, error) {
	page, err := QueryInt(r, "page", 1)
	if err != nil {
		return domain.PageRequest{}, err
	}
	size, err := QueryInt(r, "page_size", domain.DefaultPageSize)
	if err != nil {
		return domain.PageRequest{}, err
	}
	direction := domain.SortDirection(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort_dir"))))
	return domain.PageRequest{
		Page:      page,
		PageSize:  size,
		SortField: strings.TrimSpace(r.URL.Query().Get("sort_by")),
		SortDir:   direction,
	}, nil
}

// ListEnvelope is the uniform list payload.
type ListEnvelope struct {
	Items any             `json:"items"`
	Meta  domain.PageMeta `json:"meta"`
}
