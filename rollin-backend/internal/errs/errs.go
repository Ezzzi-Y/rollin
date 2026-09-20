// Package errs defines the single error vocabulary shared by every layer: the code
// enumeration and the fixed HTTP mapping of 04-api-contract.md §1.3, and the uniform
// error body {code, message, details?}. Service code returns *Error values (or wraps
// them); the HTTP layer renders them verbatim — messages are written for users, internal
// details never leak (anything not an *Error renders as INTERNAL_ERROR).
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable error identifier from the contract enumeration. Never reorder the
// strings: they are part of the API surface.
type Code string

const (
	CodeValidation         Code = "VALIDATION_ERROR"
	CodeUnauthenticated    Code = "UNAUTHENTICATED"
	CodeTokenExpired       Code = "TOKEN_EXPIRED"
	CodeForbidden          Code = "FORBIDDEN"
	CodeActivityDisabled   Code = "ACTIVITY_DISABLED"
	CodeActivityArchived   Code = "ACTIVITY_ARCHIVED"
	CodeRankingFrozen      Code = "RANKING_FROZEN"
	CodeNotFound           Code = "NOT_FOUND"
	CodeTokenInvalid       Code = "TOKEN_INVALID"
	CodeConflict           Code = "CONFLICT"
	CodeEmailTaken         Code = "EMAIL_TAKEN"
	CodeMemberExists       Code = "MEMBER_EXISTS"
	CodeSlugTaken          Code = "SLUG_TAKEN"
	CodeQuotaExceeded      Code = "QUOTA_EXCEEDED"
	CodeQuotaTooSmall      Code = "QUOTA_TOO_SMALL"
	CodeOfferNotActionable Code = "OFFER_NOT_ACTIONABLE"
	CodeRankingDirty       Code = "RANKING_DIRTY"
	CodeOfferExpired       Code = "OFFER_EXPIRED"
	CodeModeLocked         Code = "MODE_LOCKED"
	CodeSMTPNotConfigured  Code = "SMTP_NOT_CONFIGURED"
	CodeRateLimited        Code = "RATE_LIMITED"
	CodeExportTooLarge     Code = "EXPORT_TOO_LARGE"
	CodeInternal           Code = "INTERNAL_ERROR"
)

// httpStatus is the fixed code→status mapping from 04-api-contract.md §1.3.
var httpStatus = map[Code]int{
	CodeValidation:         http.StatusBadRequest,
	CodeUnauthenticated:    http.StatusUnauthorized,
	CodeTokenExpired:       http.StatusUnauthorized,
	CodeForbidden:          http.StatusForbidden,
	CodeActivityDisabled:   http.StatusForbidden,
	CodeActivityArchived:   http.StatusForbidden,
	CodeRankingFrozen:      http.StatusForbidden,
	CodeNotFound:           http.StatusNotFound,
	CodeTokenInvalid:       http.StatusNotFound,
	CodeConflict:           http.StatusConflict,
	CodeEmailTaken:         http.StatusConflict,
	CodeMemberExists:       http.StatusConflict,
	CodeSlugTaken:          http.StatusConflict,
	CodeQuotaExceeded:      http.StatusConflict,
	CodeQuotaTooSmall:      http.StatusConflict,
	CodeOfferNotActionable: http.StatusConflict,
	CodeRankingDirty:       http.StatusUnprocessableEntity,
	CodeOfferExpired:       http.StatusGone,
	CodeModeLocked:         http.StatusConflict,
	CodeSMTPNotConfigured:  http.StatusConflict,
	CodeRateLimited:        http.StatusTooManyRequests,
	CodeExportTooLarge:     http.StatusRequestEntityTooLarge,
	CodeInternal:           http.StatusInternalServerError,
}

// HTTPStatus maps a code to its contractual HTTP status; unknown codes are 500.
func HTTPStatus(code Code) int {
	if status, ok := httpStatus[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// Error is the failure value every layer agrees on. Details is optional structured
// supplement (e.g. {"quota": 0}); an empty details map is never serialized.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

// New builds a contract error.
func New(code Code, message string) *Error { return &Error{Code: code, Message: message} }

// Newf builds a contract error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap keeps an existing error's message but retags it with a contract code, so
// sentinel errors from repositories can be turned into API failures at the service edge.
func Wrap(code Code, err error) *Error {
	if err == nil {
		return New(code, string(code))
	}
	return &Error{Code: code, Message: err.Error()}
}

// From extracts a *Error from an error chain, falling back to INTERNAL_ERROR with a
// generic message (never leak internals the caller did not write for users).
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return New(CodeInternal, "服务器内部错误，请稍后重试")
}

// Is reports whether err carries exactly the given code.
func Is(err error, code Code) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}

// Convenience constructors for the most common shapes.
func Validation(message string) *Error      { return New(CodeValidation, message) }
func Validationf(f string, a ...any) *Error { return Newf(CodeValidation, f, a...) }
func Unauthenticated(message string) *Error { return New(CodeUnauthenticated, message) }
func Forbidden(message string) *Error       { return New(CodeForbidden, message) }
func NotFound(message string) *Error        { return New(CodeNotFound, message) }
func Conflict(message string) *Error        { return New(CodeConflict, message) }
func Conflictf(f string, a ...any) *Error   { return Newf(CodeConflict, f, a...) }
func Internal(message string) *Error        { return New(CodeInternal, message) }

// ErrNotImplemented marks domain skeletons that later phases (P2–P5) will fill. It never
// reaches a client as a contract error; the HTTP layer maps it to INTERNAL_ERROR.
var ErrNotImplemented = errors.New("not implemented: reserved for a later phase")

// NotImplemented wraps ErrNotImplemented with the signature being stubbed, so the next
// phase can grep exactly what to fill in.
func NotImplemented(what string) error {
	return fmt.Errorf("%s: %w", what, ErrNotImplemented)
}
