package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/application"
	"rollin-backend/internal/errs"
)

// mountImport registers the external single-candidate import API (04-api-contract.md §8).
// Authentication is the Bearer rt_ import token — no cookie, no CSRF (middleware.go
// exempts /api/import/*), but per-token rate limiting applies (03 §4.4: 60 次/分钟，幂等
// 保证重试安全).
//
// Route inventory:
//
//	POST /api/import/candidates   (§8.1; single object; idempotent; 201/200)
func (s *Server) mountImport(r chi.Router) {
	r.Route("/api/import", func(importRoutes chi.Router) {
		importRoutes.Post("/candidates", s.importCandidate)
	})
}

// Import rate limits (03-permissions.md §4.4): per token identity, 60 requests per
// minute. The limiter fails open on Redis outages (ratelimit package doc).
const (
	importRateBucket  = "import"
	importRateLimit   = 60
	importRateWindow  = time.Minute
	importBearerAuth  = "Authorization"
	importBearerToken = "Bearer "
)

// importCandidateRequest is the §8.1 single-object payload. Arrays and unknown fields
// (activityId / rank / anything else) are rejected before the service is called.
type importCandidateRequest struct {
	StudentID string `json:"studentId"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Score     *int64 `json:"score"`
}

func (s *Server) importCandidate(w http.ResponseWriter, r *http.Request) {
	rawToken := bearerToken(r)
	if rawToken == "" {
		writeError(w, r, errs.New(errs.CodeTokenInvalid, "缺少 Bearer 导入令牌"))
		return
	}

	// Throttle before touching the database (03 §4.4). Identity is the raw token (the
	// limiter hashes it into the bucket key); without one, fall back to the client IP.
	if s.deps.RateLimiter != nil {
		identity := rawToken
		if identity == "" {
			identity = remoteIP(r)
		}
		allowed, _ := s.deps.RateLimiter.Allow(r.Context(), importRateBucket, identity, importRateLimit, importRateWindow)
		if !allowed {
			writeError(w, r, errs.New(errs.CodeRateLimited, "导入请求过于频繁，请稍后重试"))
			return
		}
	}

	var body importCandidateRequest
	if err := decodeSingleObject(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	input := application.ImportInput{
		StudentID: body.StudentID,
		Name:      body.Name,
		Email:     body.Email,
	}
	if body.Score != nil {
		input.Score = int(*body.Score) // 64-bit int: validated 1..2147483647 in the service
	}

	result, err := s.deps.Applications.ImportOne(r.Context(), rawToken, input)
	if err != nil {
		writeError(w, r, err)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated // 04 §8.1: 201 新建 / 200 幂等命中
	}
	writeJSON(w, status, map[string]any{
		"created":       result.Created,
		"applicationId": result.ApplicationID,
		"candidateId":   result.CandidateID,
		"activityId":    result.ActivityID,
		"status":        result.Status,
		"rankingDirty":  result.RankingDirty,
	})
}

// bearerToken extracts the raw import token from the Authorization header; the token
// must never travel in the URL (需求 22 章), so query parameters are ignored entirely.
func bearerToken(r *http.Request) string {
	header := r.Header.Get(importBearerAuth)
	if !strings.HasPrefix(header, importBearerToken) {
		return ""
	}
	return strings.TrimSpace(header[len(importBearerToken):])
}

// decodeSingleObject decodes strictly (unknown fields rejected) and refuses JSON arrays
// up front with a dedicated message (04 §8.1: 数组 → VALIDATION_ERROR；禁止批量).
func decodeSingleObject(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return errs.Validation("请求体读取失败")
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return errs.Validation("仅支持单个候选人对象，不支持数组批量导入")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errs.Validation("请求体不是合法的 JSON、包含未知字段或缺少必填字段")
	}
	return nil
}
