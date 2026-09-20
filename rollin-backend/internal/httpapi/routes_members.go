package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/member"
	"rollin-backend/internal/model"
)

// Shared member-management handlers (04 §2.2/§3.4/§3.5 for OWNERs and §5.11 for ADMINs).
// The request/response shapes are identical for both callers; only the invited role, the
// SMTP scope behind it and the actor differ. The platform path invites OWNERs (PLATFORM
// SMTP, strict gate); the OWNER path invites ADMINs (ACTIVITY SMTP, member creation not
// blocked per P2 deviation — the SMTP failure is reported in-band with the created ids).

// resolveActivity resolves the {slug} URL parameter for handlers outside the activity
// workspace middleware (the platform surface does not pre-bind the scope).
func (s *Server) resolveActivity(w http.ResponseWriter, r *http.Request) (activityID uint64, ok bool) {
	activity, err := s.deps.Activity.GetBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		writeError(w, r, errs.NotFound("活动不存在"))
		return 0, false
	}
	return activity.ID, true
}

// membersListHandler renders GET .../owners and GET /api/activities/{slug}/members
// (04 §5.11 shape): paging plus the current PENDING invitation per member.
func (s *Server) membersListHandler(w http.ResponseWriter, r *http.Request) {
	activityID, ok := s.resolveActivity(w, r)
	if !ok {
		return
	}
	page := parseIntDefault(r.URL.Query().Get("page"), 1)
	pageSize := parseIntDefault(r.URL.Query().Get("pageSize"), 20)
	if pageSize > 200 {
		pageSize = 200
	}
	rows, total, err := s.deps.Member.ListMembers(r.Context(), activityID, page, pageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"userId":        row.UserID,
			"name":          row.Name,
			"email":         row.Email,
			"role":          row.Role,
			"memberStatus":  row.MemberStatus,
			"accountStatus": row.AccountStatus,
			"createdAt":     rfc3339(row.CreatedAt),
		}
		if row.Invitation != nil {
			item["invitation"] = map[string]any{
				"invitationId": row.Invitation.InviteTokenID,
				"status":       row.Invitation.Status,
				"expiresAt":    rfc3339(row.Invitation.ExpiresAt),
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
	})
}

type memberInviteRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// memberInviteHandler implements POST .../owners (role OWNER, platform caller) and
// POST /api/activities/{slug}/members (role ADMIN, OWNER caller).
func (s *Server) memberInviteHandler(w http.ResponseWriter, r *http.Request, role string) {
	var body memberInviteRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	activityID, ok := s.resolveActivity(w, r)
	if !ok {
		return
	}
	principal, _ := principalFrom(r.Context())
	var invited *member.Invited
	var err error
	if role == model.MemberRoleOwner {
		invited, err = s.deps.Member.InviteOwner(r.Context(), principal.ID, activityID, body.Name, body.Email)
	} else {
		invited, err = s.deps.Member.InviteAdmin(r.Context(), principal.ID, activityID, body.Name, body.Email)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !invited.MailQueued {
		// P2 deviation (08 notes §8): the member and the one-shot token stand; only the
		// mail queue step was skipped because the activity SMTP is missing/unverified.
		writeError(w, r, errs.New(errs.CodeSMTPNotConfigured,
			"活动 SMTP 未配置或未验证，邀请邮件暂无法发送；成员已创建，可配置 SMTP 后重发邀请").
			WithDetails(map[string]any{
				"userId":       invited.UserID,
				"memberId":     invited.MemberID,
				"invitationId": invited.InviteTokenID,
				"email":        invited.Email,
			}))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"userId":       invited.UserID,
		"memberId":     invited.MemberID,
		"invitationId": invited.InviteTokenID,
		"email":        invited.Email,
	})
}

// memberResendHandler implements the .../invitation/resend endpoints (04 §3.5/§5.11):
// 202 with the fresh invitation id; only still-INVITED accounts qualify.
func (s *Server) memberResendHandler(w http.ResponseWriter, r *http.Request) {
	activityID, ok := s.resolveActivity(w, r)
	if !ok {
		return
	}
	userID, ok := parseIDParam(w, r, "userId")
	if !ok {
		return
	}
	principal, _ := principalFrom(r.Context())
	invited, err := s.deps.Member.ResendInvitation(r.Context(), principal.ID, activityID, userID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !invited.MailQueued {
		writeError(w, r, errs.New(errs.CodeSMTPNotConfigured,
			"活动 SMTP 未配置或未验证，邀请邮件暂无法发送；新邀请链接已生成").
			WithDetails(map[string]any{"invitationId": invited.InviteTokenID}))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"invitationId": invited.InviteTokenID})
}

// memberDisableHandler implements POST .../owners/{userId}/disable (platform, OWNER
// target) and POST /api/activities/{slug}/members/{userId}/disable (OWNER, ADMIN target).
func (s *Server) memberDisableHandler(w http.ResponseWriter, r *http.Request, callerIsPlatform bool, message string) {
	activityID, ok := s.resolveActivity(w, r)
	if !ok {
		return
	}
	userID, ok := parseIDParam(w, r, "userId")
	if !ok {
		return
	}
	principal, _ := principalFrom(r.Context())
	if err := s.deps.Member.DisableMember(r.Context(), principal.ID, activityID, userID, callerIsPlatform); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": message, "memberStatus": model.MemberDisabled})
}

func parseIDParam(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	parsed, err := strconv.ParseUint(chi.URLParam(r, name), 10, 64)
	if err != nil || parsed == 0 {
		writeError(w, r, errs.NotFound("成员不存在"))
		return 0, false
	}
	return parsed, true
}
