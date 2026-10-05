package migrations

func v7() Migration {
	return Migration{Version: 7, Name: "V7__platform_refill_pause", Steps: []Step{
		{Name: "activity_platform_refill_pause", SQL: "ALTER TABLE activity ADD COLUMN refill_paused_by_platform TINYINT(1) NOT NULL DEFAULT 0"},
		{Name: "backfill_platform_refill_pause", SQL: `UPDATE activity a SET a.refill_paused_by_platform = 1 WHERE a.refill_paused = 1 AND EXISTS (SELECT 1 FROM audit_log l WHERE l.activity_id = a.id AND l.action = 'REFILL_PAUSED' AND l.actor_type = 'SUPER_ADMIN' AND NOT EXISTS (SELECT 1 FROM audit_log newer WHERE newer.activity_id = a.id AND newer.action IN ('REFILL_PAUSED', 'REFILL_RESUMED') AND newer.id > l.id))`},
	}}
}
