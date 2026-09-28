package migrations

// v6 expands audit_log.user_agent to accommodate long enterprise mobile-browser
// user-agent strings (for example, WeCom's embedded Android browser).
func v6() Migration {
	return Migration{
		Version: 6,
		Name:    "V6__expand_audit_user_agent",
		Steps: []Step{{
			Name: "audit_expand_user_agent",
			SQL:  "ALTER TABLE audit_log MODIFY user_agent VARCHAR(512) NULL",
		}},
	}
}
