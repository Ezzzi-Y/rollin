// Package policy is the SINGLE decision point for every authorization question that
// does not require a database round trip (03-permissions.md §1–§3):
//
//   - which activity status (ACTIVE/DISABLED/ARCHIVED) permits which operation class;
//   - whether a member status may proceed;
//   - whether a role satisfies a required minimum role.
//
// P3/P4/P5 MUST route their endpoint guards through this package instead of
// hand-writing `if status == ...` checks, so the DISABLED/ARCHIVED matrix of
// 03-permissions.md §3 stays defined in exactly one file. Database-backed checks
// (session validity, member row existence) stay in the middleware; this package only
// combines already-fetched facts into a verdict.
//
// All verdicts are either nil (proceed) or a contract *errs.Error ready to render.
package policy

import (
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

// Operation is the endpoint class a request belongs to (03-permissions.md §3 matrix).
type Operation string

const (
	// OpAuth covers session establishment and the always-available session endpoints
	// (activity login, me, logout). DISABLED denies it; ARCHIVED allows it.
	OpAuth Operation = "AUTH"
	// OpRead covers every read-only business surface, including exports and the
	// public offer GET (ARCHIVED keeps serving all of them, A18).
	OpRead Operation = "READ"
	// OpWrite covers every mutating business surface. DISABLED and ARCHIVED both
	// deny it, with distinct contract codes.
	OpWrite Operation = "WRITE"
)

// AnyRole is the required-role wildcard: any active member role qualifies.
const AnyRole = ""

// ActivityGate evaluates the activity-status column of the 03 §3 matrix:
//
//	ACTIVE    → every operation allowed.
//	DISABLED  → everything denied with ACTIVITY_DISABLED (88.1.6: members cannot
//	            enter, offers are "已失效"; no request may touch the activity).
//	ARCHIVED  → reads/auth allowed; writes denied with ACTIVITY_ARCHIVED (终态只读).
func ActivityGate(status string, op Operation) error {
	switch status {
	case model.ActivityActive:
		return nil
	case model.ActivityDisabled:
		return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法访问")
	case model.ActivityArchived:
		if op == OpWrite {
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		}
		return nil
	default:
		return errs.New(errs.CodeInternal, "活动状态异常")
	}
}

// MemberGate requires the activity_member row to be ACTIVE. A DISABLED member loses
// access immediately (real-time session recheck, 03 §4.2) — the platform never disables
// the account itself (88.1.7), only the membership.
func MemberGate(memberStatus string) error {
	if memberStatus != model.MemberActive {
		return errs.Forbidden("成员已被停用，无法访问该活动")
	}
	return nil
}

// RoleGate checks the operation-level role requirement. The caller's role must be a real
// member role (OWNER/ADMIN) in every case — an empty or foreign role string (e.g. a
// platform principal) never passes, even against AnyRole. required=AnyRole accepts any
// member role; required=ADMIN accepts OWNER or ADMIN (O 恒满足 A 要求); required=OWNER
// accepts only OWNER.
func RoleGate(role, required string) error {
	switch role {
	case model.MemberRoleOwner, model.MemberRoleAdmin:
	default:
		return errs.Forbidden("没有执行该操作的权限")
	}
	switch required {
	case AnyRole:
		return nil
	case model.MemberRoleAdmin:
		if role == model.MemberRoleOwner || role == model.MemberRoleAdmin {
			return nil
		}
	case model.MemberRoleOwner:
		if role == model.MemberRoleOwner {
			return nil
		}
	}
	return errs.Forbidden("没有执行该操作的权限")
}

// Access is the fully-materialized authorization triple of one request: the activity
// status, the member status and the role resolved in real time by the session
// middleware. Endpoints (and P3/P4/P5 services) authorize through Authorize.
type Access struct {
	ActivityStatus string
	MemberStatus   string
	Role           string
}

// Authorize applies the full gate chain in precedence order: the activity status first
// (a DISABLED activity reports ACTIVITY_DISABLED even for a wrong role — 88.1.6 makes
// entering impossible), then the member status, then the role requirement.
func (a Access) Authorize(op Operation, requiredRole string) error {
	if err := ActivityGate(a.ActivityStatus, op); err != nil {
		return err
	}
	if err := MemberGate(a.MemberStatus); err != nil {
		return err
	}
	return RoleGate(a.Role, requiredRole)
}
