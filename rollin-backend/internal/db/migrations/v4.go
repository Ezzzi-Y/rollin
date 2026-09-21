package migrations

// v4 introduces the BATCH（分批发放）offer mode and the candidate identity column for
// audit rows written by the public token paths:
//   - activity.offer_mode gains the 'BATCH' value and the batch_size column (0 =
//     inherit the platform defaultBatchSize setting, resolved at read time);
//   - offer.source gains 'BATCH' and offer.batch_id links each batch offer to its
//     offer_batch record ("第 N 批，发放 M 人，操作人、时间");
//   - audit_log.actor_candidate_id records WHOSE token performed an accept/decline so
//     the OWNER console can show the acting candidate (name resolved from application
//     at read time, never denormalized);
//   - the defaultBatchSize platform setting is seeded for both fresh and existing DBs.
//
// ENUM modifications only append values, which MySQL applies as an in-place metadata
// change; every step is idempotent-safe to re-run after an interrupted execution.
func v4() Migration {
	return Migration{
		Version: 4,
		Name:    "V4__batch_mode_and_candidate_audit",
		Steps: []Step{
			{
				Name: "activity_mode_add_batch",
				SQL:  "ALTER TABLE activity MODIFY offer_mode ENUM('AUTO','MANUAL','BATCH') NOT NULL DEFAULT 'AUTO'",
			},
			{
				Name: "activity_add_batch_size",
				SQL: "ALTER TABLE activity ADD COLUMN batch_size INT NOT NULL DEFAULT 0 AFTER offer_mode, " +
					"ADD CONSTRAINT chk_activity_batch_size CHECK (batch_size BETWEEN 0 AND 10000)",
			},
			{
				Name: "create_offer_batch",
				SQL: `CREATE TABLE IF NOT EXISTS offer_batch (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  activity_id BIGINT UNSIGNED NOT NULL,
  batch_no INT UNSIGNED NOT NULL,
  issued_count INT UNSIGNED NOT NULL DEFAULT 0,
  created_by_user_id BIGINT UNSIGNED NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_offer_batch_activity_no (activity_id, batch_no),
  CONSTRAINT fk_offer_batch_activity FOREIGN KEY (activity_id) REFERENCES activity (id)
)` + tableOptions,
			},
			{
				Name: "offer_source_add_batch",
				SQL:  "ALTER TABLE offer MODIFY source ENUM('AUTO','MANUAL','SPECIAL','BATCH') NOT NULL DEFAULT 'AUTO'",
			},
			{
				Name: "offer_add_batch_id",
				SQL: "ALTER TABLE offer ADD COLUMN batch_id BIGINT UNSIGNED NULL AFTER source, " +
					"ADD KEY idx_offer_batch (batch_id), " +
					"ADD CONSTRAINT fk_offer_batch FOREIGN KEY (batch_id) REFERENCES offer_batch (id)",
			},
			{
				Name: "audit_add_candidate_actor",
				SQL:  "ALTER TABLE audit_log ADD COLUMN actor_candidate_id BIGINT UNSIGNED NULL AFTER actor_user_id",
			},
			{
				Name: "seed_default_batch_size",
				SQL:  "INSERT IGNORE INTO platform_setting (`key`, value) VALUES ('defaultBatchSize', '20')",
			},
		},
	}
}
