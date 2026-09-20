package migrations

// v3 stores the additional activity-local candidate contact/profile fields accepted by
// POST /api/import/candidates. Existing rows receive empty strings so the migration is
// safe for already-imported candidates and remains compatible with the pre-v3 payload.
func v3() Migration {
	return Migration{
		Version: 3,
		Name:    "V3__candidate_profile_fields",
		Steps: []Step{
			{
				Name: "application_add_qq",
				SQL:  "ALTER TABLE application ADD COLUMN qq VARCHAR(32) NOT NULL DEFAULT '' AFTER email",
			},
			{
				Name: "application_add_class_name",
				SQL:  "ALTER TABLE application ADD COLUMN class_name VARCHAR(100) NOT NULL DEFAULT '' AFTER qq",
			},
		},
	}
}
