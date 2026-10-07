package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// platformExportCandidates is mounted only behind requirePlatformSession. Platform
// downloads include DISABLED and ARCHIVED directions; activity-member routes keep
// their existing membership and activity-status gates.
func (s *Server) platformExportCandidates(w http.ResponseWriter, r *http.Request) {
	var workbook bytes.Buffer
	slug := chi.URLParam(r, "slug")
	prefix := "all"
	if slug != "" {
		act, err := s.deps.Activity.GetBySlug(r.Context(), slug)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if err := s.deps.Export.ExportCandidatesXLSX(r.Context(), act.ID, &workbook); err != nil {
			writeError(w, r, err)
			return
		}
		prefix = act.Slug
	} else {
		if err := s.deps.Export.ExportAllCandidatesXLSX(r.Context(), &workbook); err != nil {
			writeError(w, r, err)
			return
		}
	}
	// Finalize before setting download headers so failures remain normal JSON errors.
	date := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("20060102")
	filename := fmt.Sprintf("%s-candidates-%s.xlsx", prefix, date)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = workbook.WriteTo(w)
}
