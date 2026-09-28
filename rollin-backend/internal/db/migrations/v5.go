package migrations

// v5 stores optional candidate feedback after a candidate actively declines an Offer.
func v5() Migration {
	return Migration{
		Version: 5,
		Name:    "V5__offer_decline_feedback",
		Steps: []Step{{
			Name: "offer_add_decline_feedback",
			SQL: "ALTER TABLE offer ADD COLUMN decline_reason VARCHAR(500) NULL AFTER reason, " +
				"ADD COLUMN decline_reason_at DATETIME NULL AFTER decline_reason, " +
				"ADD COLUMN decline_source ENUM('','CANDIDATE','CROSS_ACTIVITY','SYSTEM') NOT NULL DEFAULT '' AFTER decline_reason_at",
		}},
	}
}
