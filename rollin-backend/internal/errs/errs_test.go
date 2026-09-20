package errs

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestHTTPStatusMapping(t *testing.T) {
	// Spot-check the fixed mapping of 04-api-contract.md §1.3.
	cases := map[Code]int{
		CodeValidation:       http.StatusBadRequest,
		CodeUnauthenticated:  http.StatusUnauthorized,
		CodeTokenExpired:     http.StatusUnauthorized,
		CodeForbidden:        http.StatusForbidden,
		CodeActivityDisabled: http.StatusForbidden,
		CodeRankingFrozen:    http.StatusForbidden,
		CodeNotFound:         http.StatusNotFound,
		CodeTokenInvalid:     http.StatusNotFound,
		CodeConflict:         http.StatusConflict,
		CodeEmailTaken:       http.StatusConflict,
		CodeQuotaExceeded:    http.StatusConflict,
		CodeRankingDirty:     http.StatusUnprocessableEntity,
		CodeOfferExpired:     http.StatusGone,
		CodeRateLimited:      http.StatusTooManyRequests,
		CodeExportTooLarge:   http.StatusRequestEntityTooLarge,
		CodeInternal:         http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := HTTPStatus(code); got != want {
			t.Fatalf("HTTPStatus(%s) = %d, want %d", code, got, want)
		}
	}
}

func TestErrorBodyShape(t *testing.T) {
	e := New(CodeValidation, "录取名额必须大于 0").WithDetails(map[string]any{"quota": 0})
	body := map[string]any{}
	data, err := json.Marshal(map[string]any{"code": e.Code, "message": e.Message, "details": e.Details})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "VALIDATION_ERROR" || body["message"] != "录取名额必须大于 0" {
		t.Fatalf("unexpected body: %s", data)
	}
	details, ok := body["details"].(map[string]any)
	if !ok || details["quota"] != float64(0) {
		t.Fatalf("unexpected details: %s", data)
	}
}

func TestFromAndIs(t *testing.T) {
	original := Conflict("已处理")
	wrapped := errors.Join(errors.New("outer"), original)
	if !Is(wrapped, CodeConflict) {
		t.Fatal("Is must unwrap the chain")
	}
	if Is(wrapped, CodeNotFound) {
		t.Fatal("Is must not match a different code")
	}
	generic := From(errors.New("db exploded"))
	if generic.Code != CodeInternal {
		t.Fatalf("unknown errors must map to INTERNAL_ERROR, got %s", generic.Code)
	}
	if From(original).Code != CodeConflict {
		t.Fatal("From must preserve the contract error")
	}
}

func TestNotImplementedIsDistinct(t *testing.T) {
	err := NotImplemented("x.Service.Y")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("NotImplemented must wrap the sentinel")
	}
	var e *Error
	if errors.As(From(err), &e) && e.Code != CodeInternal {
		t.Fatal("unhandled not-implemented errors must render as INTERNAL_ERROR")
	}
}
