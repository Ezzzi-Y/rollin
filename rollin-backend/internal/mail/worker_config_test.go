package mail

import (
	"testing"
	"time"
)

// DefaultWorkerConfig maps unset deployment knobs to the defaults (15s scans, 30s
// submissions) and passes explicit values through. The per-host send rest is fixed
// at twelve seconds (每服务器每分钟 5 封) — the scan cadence fed in from config
// (6s) must stay at or below it, since a scan sends at most one mail per host.
func TestDefaultWorkerConfigDefaults(t *testing.T) {
	cfg := DefaultWorkerConfig(0, 0)
	if cfg.Every != 15*time.Second || cfg.SendTimeout != 30*time.Second {
		t.Fatalf("zero knobs must fall back to the defaults: %+v", cfg)
	}
	if cfg.SendInterval != 12*time.Second {
		t.Fatalf("the per-host send rest is contractual at 12s (5 mails/min), got %v", cfg.SendInterval)
	}
	if cfg.Lease != LeaseTimeout || cfg.Batch != 20 || cfg.BackoffBase != time.Minute || cfg.BackoffMax != time.Hour {
		t.Fatalf("contractual fields must keep their values: %+v", cfg)
	}

	cfg = DefaultWorkerConfig(6*time.Second, time.Minute)
	if cfg.Every != 6*time.Second || cfg.SendTimeout != time.Minute || cfg.SendInterval != 12*time.Second {
		t.Fatalf("explicit knobs must pass through with the fixed send rest: %+v", cfg)
	}
}
