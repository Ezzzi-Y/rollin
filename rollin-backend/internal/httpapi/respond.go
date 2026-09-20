package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gorm.io/gorm"
	"rollin-backend/internal/errs"
)

// decodeJSON reads a strictly-shaped JSON body: unknown fields are rejected and the size
// is capped. Validation errors surface as VALIDATION_ERROR with the decoder's message.
func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errs.Validation("请求体不是合法的 JSON 或包含未知字段")
	}
	return nil
}

// writeJSON renders any value with the contractual charset header.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError renders the uniform error body {code, message, details?}. Everything that
// is not an errs.Error (including gorm.ErrRecordNotFound and the not-implemented stubs)
// is mapped defensively so internals never leak.
func writeError(w http.ResponseWriter, _ *http.Request, err error) {
	var e *errs.Error
	switch {
	case errors.As(err, &e):
	case errors.Is(err, gorm.ErrRecordNotFound):
		e = errs.NotFound("资源不存在或无权访问")
	case errors.Is(err, errs.ErrNotImplemented):
		e = errs.Internal("该功能尚未开放")
	default:
		e = errs.Internal("服务器内部错误，请稍后重试")
	}
	body := map[string]any{"code": e.Code, "message": e.Message}
	if len(e.Details) > 0 {
		body["details"] = e.Details
	}
	writeJSON(w, errs.HTTPStatus(e.Code), body)
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, errs.NotFound("资源不存在或无权访问"))
}

// errsUnauthenticated is the standard 401 the session middleware stubs emit.
func errsUnauthenticated() error { return errs.Unauthenticated("登录状态无效或已过期") }

func methodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, errs.Validation("请求方法不支持"))
}
