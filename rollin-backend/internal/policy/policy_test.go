package policy

import (
	"errors"
	"testing"

	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

// TestActivityGateMatrix pins the DISABLED/ARCHIVED behavior matrix of
// 03-permissions.md §3 verbatim — it is the single source every phase routes through.
func TestActivityGateMatrix(t *testing.T) {
	cases := []struct {
		status string
		op     Operation
		code   errs.Code // "" means allowed
	}{
		{model.ActivityActive, OpAuth, ""},
		{model.ActivityActive, OpRead, ""},
		{model.ActivityActive, OpWrite, ""},

		// DISABLED denies everything: login, reads, writes (88.1.6).
		{model.ActivityDisabled, OpAuth, errs.CodeActivityDisabled},
		{model.ActivityDisabled, OpRead, errs.CodeActivityDisabled},
		{model.ActivityDisabled, OpWrite, errs.CodeActivityDisabled},

		// ARCHIVED: login + reads + export stay available, writes are rejected.
		{model.ActivityArchived, OpAuth, ""},
		{model.ActivityArchived, OpRead, ""},
		{model.ActivityArchived, OpWrite, errs.CodeActivityArchived},

		// Unknown status is a server-side anomaly, never silently allowed.
		{"", OpRead, errs.CodeInternal},
		{"SUSPENDED", OpWrite, errs.CodeInternal},
	}
	for _, tc := range cases {
		err := ActivityGate(tc.status, tc.op)
		if tc.code == "" {
			if err != nil {
				t.Fatalf("ActivityGate(%s,%s) = %v, want nil", tc.status, tc.op, err)
			}
			continue
		}
		if !errs.Is(err, tc.code) {
			t.Fatalf("ActivityGate(%s,%s) = %v, want code %s", tc.status, tc.op, err, tc.code)
		}
	}
}

// TestMemberGate pins the real-time disable semantics: only ACTIVE members proceed.
func TestMemberGate(t *testing.T) {
	if err := MemberGate(model.MemberActive); err != nil {
		t.Fatalf("active member must pass, got %v", err)
	}
	for _, status := range []string{model.MemberDisabled, ""} {
		err := MemberGate(status)
		if !errs.Is(err, errs.CodeForbidden) {
			t.Fatalf("MemberGate(%q) = %v, want FORBIDDEN", status, err)
		}
	}
}

// TestRoleGate pins OWNER ⊃ ADMIN ⊀ role ladder (03-permissions.md §2).
func TestRoleGate(t *testing.T) {
	cases := []struct {
		role     string
		required string
		allowed  bool
	}{
		{model.MemberRoleOwner, model.MemberRoleOwner, true},
		{model.MemberRoleOwner, model.MemberRoleAdmin, true}, // O 恒满足 A
		{model.MemberRoleOwner, AnyRole, true},
		{model.MemberRoleAdmin, model.MemberRoleAdmin, true},
		{model.MemberRoleAdmin, model.MemberRoleOwner, false}, // ADMIN 无租户级高权限
		{model.MemberRoleAdmin, AnyRole, true},
		{"", AnyRole, false}, // 无角色不得通过
		{"", model.MemberRoleAdmin, false},
		{"SUPER_ADMIN", model.MemberRoleAdmin, false}, // 平台作用域不映射活动角色
	}
	for _, tc := range cases {
		err := RoleGate(tc.role, tc.required)
		if tc.allowed && err != nil {
			t.Fatalf("RoleGate(%s,%s) = %v, want nil", tc.role, tc.required, err)
		}
		if !tc.allowed {
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != errs.CodeForbidden {
				t.Fatalf("RoleGate(%s,%s) = %v, want FORBIDDEN", tc.role, tc.required, err)
			}
		}
	}
}

// TestAccessAuthorizeOrder pins the precedence: activity status wins over member and
// role failures, so a DISABLED activity never leaks FORBIDDEN-shaped role details.
func TestAccessAuthorizeOrder(t *testing.T) {
	// DISABLED dominates a disabled member and a missing role.
	err := Access{ActivityStatus: model.ActivityDisabled, MemberStatus: model.MemberDisabled, Role: ""}.
		Authorize(OpWrite, model.MemberRoleOwner)
	if !errs.Is(err, errs.CodeActivityDisabled) {
		t.Fatalf("DISABLED must dominate, got %v", err)
	}

	// ARCHIVED dominates the role failure for writes.
	err = Access{ActivityStatus: model.ActivityArchived, MemberStatus: model.MemberActive, Role: model.MemberRoleAdmin}.
		Authorize(OpWrite, model.MemberRoleOwner)
	if !errs.Is(err, errs.CodeActivityArchived) {
		t.Fatalf("ARCHIVED write must win over role check, got %v", err)
	}

	// Disabled member on an ACTIVE activity → FORBIDDEN.
	err = Access{ActivityStatus: model.ActivityActive, MemberStatus: model.MemberDisabled, Role: model.MemberRoleOwner}.
		Authorize(OpRead, AnyRole)
	if !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("disabled member must be FORBIDDEN, got %v", err)
	}

	// The happy paths.
	ok := []Access{
		{ActivityStatus: model.ActivityActive, MemberStatus: model.MemberActive, Role: model.MemberRoleAdmin},
		{ActivityStatus: model.ActivityArchived, MemberStatus: model.MemberActive, Role: model.MemberRoleOwner},
	}
	for _, a := range ok {
		for _, op := range []Operation{OpAuth, OpRead} {
			if err := a.Authorize(op, AnyRole); err != nil {
				t.Fatalf("Access %+v %s must pass, got %v", a, op, err)
			}
		}
	}
}
