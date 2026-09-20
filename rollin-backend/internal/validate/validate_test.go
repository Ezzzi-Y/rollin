package validate

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Li@Example.edu.cn ": "li@example.edu.cn",
		"ROOT@X.COM":           "root@x.com",
		"a@b.c":                "a@b.c",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Fatalf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{"zhangsan@example.edu.cn", "a.b+c@sub.domain.edu.cn", "x@y.io"}
	invalid := []string{"", "no-at-sign", "a@b", "a b@c.d", "a@b c.d", string(make([]byte, 250)) + "@x.cn"}
	for _, value := range valid {
		if err := ValidateEmail(value); err != nil {
			t.Fatalf("ValidateEmail(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range invalid {
		if err := ValidateEmail(value); err == nil {
			t.Fatalf("ValidateEmail(%q) = nil, want error", value)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	valid := []string{"abc", "tech-2026", "a1b2c3", "act-8zk2mq4p"}
	invalid := []string{
		"", "ab", // too short
		strings.Repeat("a", 65), // too long
		"Tech-2026",             // uppercase
		"-lead", "trail-",       // hyphen edges
		"double--hyphen", // consecutive hyphens? (rejected by the pattern)
		"has space", "has_underscore", "中文", "a.b",
	}
	for _, value := range valid {
		if err := ValidateSlug(value); err != nil {
			t.Fatalf("ValidateSlug(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range invalid {
		if err := ValidateSlug(value); err == nil {
			t.Fatalf("ValidateSlug(%q) = nil, want error", value)
		}
	}
}

func TestValidateStudentID(t *testing.T) {
	valid := []string{"1", "2026010388", "0001", "A-1_2", strings.Repeat("x", 64)}
	invalid := []string{"", strings.Repeat("x", 65), "学号", "has space", "a.b"}
	for _, value := range valid {
		if err := ValidateStudentID(value); err != nil {
			t.Fatalf("ValidateStudentID(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range invalid {
		if err := ValidateStudentID(value); err == nil {
			t.Fatalf("ValidateStudentID(%q) = nil, want error", value)
		}
	}
}

func TestValidateScore(t *testing.T) {
	if err := ValidateScore(1); err != nil {
		t.Fatalf("score 1 must be valid: %v", err)
	}
	if err := ValidateScore(ScoreMax); err != nil {
		t.Fatalf("score INT max must be valid: %v", err)
	}
	for _, value := range []int{0, -1, -100, ScoreMax + 1, 1 << 31} {
		if err := ValidateScore(value); err == nil {
			t.Fatalf("ValidateScore(%d) = nil, want error", value)
		}
	}
}

func TestValidateQuotaAndExpireHours(t *testing.T) {
	if err := ValidateQuota(0); err == nil {
		t.Fatal("quota 0 must be rejected (88.7.1)")
	}
	if err := ValidateQuota(1); err != nil {
		t.Fatalf("quota 1 must be valid: %v", err)
	}
	if err := ValidateOfferExpireHours(0); err == nil {
		t.Fatal("expire hours 0 must be rejected")
	}
	if err := ValidateOfferExpireHours(721); err == nil {
		t.Fatal("expire hours 721 must be rejected")
	}
	if err := ValidateOfferExpireHours(720); err != nil {
		t.Fatalf("expire hours 720 must be valid: %v", err)
	}
}

func TestValidatePassword(t *testing.T) {
	valid := []string{"P@ssw0rd123", "abcd1234"}
	invalid := []string{"", "short1", "onlyletters", "12345678", "no digits but long enough"}
	for _, value := range valid {
		if err := ValidatePassword(value); err != nil {
			t.Fatalf("ValidatePassword(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range invalid {
		if err := ValidatePassword(value); err == nil {
			t.Fatalf("ValidatePassword(%q) = nil, want error", value)
		}
	}
	// bcrypt truncates beyond 72 bytes; the rule must reject those inputs.
	long := strings.Repeat("a", 73) + "1"
	if err := ValidatePassword(long); err == nil {
		t.Fatal("password longer than 72 bytes must be rejected")
	}
}
