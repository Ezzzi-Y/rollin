package migrations

// v2 adds the SMTP submission TLS mode. smtp.163.com and most Chinese providers expose
// submission on 465 as implicit TLS (SSL: the connection is TLS from the first byte);
// the V1 sender only did plaintext + opportunistic STARTTLS, which times out against
// such servers. Existing rows default to STARTTLS so their behavior stays unchanged.
func v2() Migration {
	return Migration{
		Version: 2,
		Name:    "V2__smtp_encryption",
		Steps: []Step{
			{
				Name: "smtp_config_add_encryption",
				SQL: "ALTER TABLE smtp_config ADD COLUMN encryption " +
					"ENUM('NONE','STARTTLS','SSL') NOT NULL DEFAULT 'STARTTLS' AFTER port",
			},
		},
	}
}
