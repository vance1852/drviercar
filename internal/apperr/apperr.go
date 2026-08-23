// Package apperr defines the error taxonomy shared by the domain, service,
// repository and HTTP layers of the road-test operations platform.
//
// Every error that crosses a layer boundary keeps its sentinel identity so
// that the HTTP layer can map it to a stable code without string matching.
package apperr

import (
	"errors"
	"fmt"
)

// Kind classifies an error for transport mapping.
type Kind string

const (
	KindInvalid      Kind = "invalid_argument"
	KindUnauthorized Kind = "unauthorized"
	KindForbidden    Kind = "forbidden"
	KindNotFound     Kind = "not_found"
	KindConflict     Kind = "conflict"
	KindPrecondition Kind = "failed_precondition"
	KindExhausted    Kind = "resource_exhausted"
	KindCancelled    Kind = "cancelled"
	KindInternal     Kind = "internal"
)

// Sentinel errors. Callers compare with errors.Is so wrapping must be preserved
// through every layer.
var (
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrUnauthenticated   = errors.New("unauthenticated")
	ErrPermissionDenied  = errors.New("permission denied")
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrVersionConflict   = errors.New("version conflict")
	ErrIllegalTransition = errors.New("illegal state transition")
	ErrPreconditionUnmet = errors.New("precondition unmet")
	ErrQuotaExceeded     = errors.New("quota exceeded")
	ErrSessionExpired    = errors.New("session expired")
	ErrIdempotencyReuse  = errors.New("idempotency key reused with a different request")
	ErrInternal          = errors.New("internal error")
)

// Error carries a machine readable code plus the operator facing message.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	cause   error
}

func (e *Error) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
}

// Unwrap keeps the sentinel chain reachable for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// New builds an error whose cause is the matching sentinel.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message, cause: sentinelFor(kind)}
}

// Wrap keeps an existing cause reachable while attaching transport metadata.
// The message accepts fmt formatting so callers can attach business context
// without losing the sentinel chain.
func Wrap(cause error, kind Kind, code, format string, args ...any) *Error {
	message := format
	if len(args) > 0 {
		message = fmt.Sprintf(format, args...)
	}
	return &Error{Kind: kind, Code: code, Message: message, cause: cause}
}

// Invalidf reports a malformed request payload or argument.
func Invalidf(code, format string, args ...any) *Error {
	return New(KindInvalid, code, fmt.Sprintf(format, args...))
}

// NotFoundf reports a missing entity.
func NotFoundf(code, format string, args ...any) *Error {
	return New(KindNotFound, code, fmt.Sprintf(format, args...))
}

// Conflictf reports a uniqueness or concurrency conflict.
func Conflictf(code, format string, args ...any) *Error {
	return New(KindConflict, code, fmt.Sprintf(format, args...))
}

// Preconditionf reports an unmet business precondition.
func Preconditionf(code, format string, args ...any) *Error {
	return New(KindPrecondition, code, fmt.Sprintf(format, args...))
}

// Internalf reports an unexpected infrastructure failure.
func Internalf(code, format string, args ...any) *Error {
	return New(KindInternal, code, fmt.Sprintf(format, args...))
}

func sentinelFor(kind Kind) error {
	switch kind {
	case KindInvalid:
		return ErrInvalidArgument
	case KindUnauthorized:
		return ErrUnauthenticated
	case KindForbidden:
		return ErrPermissionDenied
	case KindNotFound:
		return ErrNotFound
	case KindConflict:
		return ErrAlreadyExists
	case KindPrecondition:
		return ErrPreconditionUnmet
	case KindExhausted:
		return ErrQuotaExceeded
	case KindCancelled:
		return ErrInternal
	default:
		return ErrInternal
	}
}

// KindOf resolves the transport kind of an arbitrary error.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return KindInvalid
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, ErrSessionExpired):
		return KindUnauthorized
	case errors.Is(err, ErrPermissionDenied):
		return KindForbidden
	case errors.Is(err, ErrNotFound):
		return KindNotFound
	case errors.Is(err, ErrAlreadyExists), errors.Is(err, ErrVersionConflict), errors.Is(err, ErrIdempotencyReuse):
		return KindConflict
	case errors.Is(err, ErrIllegalTransition), errors.Is(err, ErrPreconditionUnmet):
		return KindPrecondition
	case errors.Is(err, ErrQuotaExceeded):
		return KindExhausted
	default:
		return KindInternal
	}
}

// CodeOf resolves the stable error code carried by err.
func CodeOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	switch KindOf(err) {
	case KindInvalid:
		return "invalid_argument"
	case KindUnauthorized:
		return "unauthenticated"
	case KindForbidden:
		return "permission_denied"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindPrecondition:
		return "failed_precondition"
	case KindExhausted:
		return "resource_exhausted"
	default:
		return "internal"
	}
}

// MessageOf resolves the operator facing message for err.
func MessageOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) && typed.Message != "" {
		return typed.Message
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
