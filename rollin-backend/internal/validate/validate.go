// Package validate holds the field-level rules every entry point shares (04-api-contract.md
// §1.1 and §1.3): email normalization, slug shape, student id shape, score bounds, quota
// and password strength. Services call these before anything touches the database so the
// same invalid value is rejected with the same message no matter which route it came from.
package validate

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// ScoreMax is the INT column ceiling from 05-data-model.md §6 (88.3.7: positive integer,
// not 0, at most INT max).
const ScoreMax = 2147483647

var (
	slugPattern      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	studentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	emailPattern     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// NormalizeEmail lowercases and trims an email before any validation or storage
// (04-api-contract.md §1.1: 请求入口统一小写 + 去首尾空格).
func NormalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ValidateEmail checks the normalized email shape and the 254-char column bound.
func ValidateEmail(value string) error {
	value = NormalizeEmail(value)
	if value == "" {
		return errors.New("邮箱不能为空")
	}
	if len(value) > 254 {
		return errors.New("邮箱过长")
	}
	if !emailPattern.MatchString(value) {
		return errors.New("邮箱格式不正确")
	}
	return nil
}

// ValidateSlug enforces the activity slug contract: ^[a-z0-9]+(-[a-z0-9]+)*$, 3–64 chars
// (04-api-contract.md §4.1). Slugs are immutable after creation.
func ValidateSlug(value string) error {
	if len(value) < 3 || len(value) > 64 {
		return errors.New("slug 长度必须为 3–64 个字符")
	}
	if !slugPattern.MatchString(value) {
		return errors.New("slug 只能包含小写字母、数字和中划线，且不能以中划线开头或结尾")
	}
	return nil
}

// GenerateSlugFallback reports whether an auto-generated slug candidate is acceptable —
// used when the client did not provide one (act-<8 random chars>).
func ValidAutoSlug(value string) bool {
	return len(value) >= 3 && len(value) <= 64 && slugPattern.MatchString(value)
}

// ValidateStudentID enforces ^[A-Za-z0-9_-]{1,64}$ (04-api-contract.md §1.1). The value
// is stored as a string so leading zeros survive; it is never modifiable afterwards.
func ValidateStudentID(value string) error {
	if !studentIDPattern.MatchString(value) {
		return errors.New("学号只能包含字母、数字、下划线和中划线，长度 1–64")
	}
	return nil
}

// ValidateScore enforces 1..2147483647 (88.3.7). 0, negatives and overflow are all invalid.
func ValidateScore(value int) error {
	if value < 1 {
		return errors.New("成绩必须为正整数")
	}
	if value > ScoreMax {
		return errors.New("成绩超出允许的最大值")
	}
	return nil
}

// ValidateQuota enforces quota >= 1 (88.7.1: 0 is not allowed).
func ValidateQuota(value int) error {
	if value < 1 {
		return errors.New("录取名额必须大于 0")
	}
	return nil
}

// ValidateOfferExpireHours enforces 1..720 (05-data-model.md §1 CHECK).
func ValidateOfferExpireHours(value int) error {
	if value < 1 || value > 720 {
		return errors.New("Offer 有效期必须为 1–720 小时")
	}
	return nil
}

// ValidateBatchSize enforces the BATCH-mode per-click issuance size: 1..1000, matching
// the activity.batch_size CHECK (0 = "inherit the platform default" and is handled by
// the caller before validating).
func ValidateBatchSize(value int) error {
	if value < 1 || value > 1000 {
		return errors.New("每批发放人数必须为 1–1000")
	}
	return nil
}

// ValidatePassword is the server-side twin of the form check (04-api-contract.md §4.5):
// at least 8 chars, must contain a letter and a digit, at most 72 bytes because bcrypt
// silently truncates beyond that.
func ValidatePassword(value string) error {
	if len(value) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(value) > 72 {
		return errors.New("密码不能超过 72 个字节")
	}
	letter, digit := false, false
	for _, char := range value {
		if unicode.IsLetter(char) {
			letter = true
		}
		if unicode.IsDigit(char) {
			digit = true
		}
	}
	if !letter || !digit {
		return errors.New("密码需要同时包含字母和数字，且长度不少于 8 位")
	}
	return nil
}
