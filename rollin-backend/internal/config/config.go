// Package config loads and validates all environment configuration (04-api-contract.md /
// 01-decisions.md). Failures here are fatal at startup: the process refuses to run with a
// half-configured environment instead of misbehaving later.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Cookie names are contractual (04-api-contract.md §1.4): platform and activity sessions
// are independent cookies and never clear each other.
const (
	PlatformSessionCookie = "rollin_platform_session"
	ActivitySessionCookie = "rollin_activity_session"
	CSRFCookieName        = "rollin_csrf"
)

type Config struct {
	Address string

	MySQLHost               string
	MySQLPort               string
	MySQLDatabase           string
	MySQLUser               string
	MySQLPassword           string
	MySQLAutoCreateDatabase bool

	RedisHost     string
	RedisPort     string
	RedisPassword string
	RedisDB       int

	// CandidateBaseURL is the t.xxx.xxx host candidates open offer links on;
	// AdminBaseURL is the management console host (falls back to CandidateBaseURL).
	CandidateBaseURL string
	AdminBaseURL     string

	// SMTP_ENC_KEY: base64 of a 32-byte AES-256-GCM key for encrypting SMTP passwords
	// (secretbox). Deliberately separate from any token material — opaque tokens carry
	// no key at all now that the ciphertext token scheme is abolished.
	SMTPEncKey []byte

	// Super admin bootstrap (platform_admin singleton, idempotent).
	SuperAdminName            string
	SuperAdminEmail           string
	SuperAdminInitialPassword string

	// Session lifetime; cookie attributes.
	SessionTTL   time.Duration
	CookieSecure bool

	// CSRF: extra origins (hostnames) allowed beside the request Host itself.
	CSRFAllowedOrigins []string

	// Login throttle (03 §4.4): IP+email fixed window, 5 per 15 minutes by default;
	// LOGIN_RATE_LIMIT / LOGIN_RATE_WINDOW_MINUTES override.
	LoginRateLimit  int
	LoginRateWindow time.Duration

	// Worker intervals (P3/P5 consumers).
	MailWorkerEvery   time.Duration
	RefillWorkerEvery time.Duration

	// Mail send timeout (P3-7): bounds one SMTP submission inside the mail worker;
	// MAIL_SMTP_TIMEOUT_SECONDS overrides the 30s default.
	MailSendTimeout time.Duration
}

func Load() (Config, error) {
	_ = godotenv.Load()
	cfg := Config{
		Address:                 env("HTTP_ADDRESS", ":8080"),
		MySQLHost:               env("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:               env("MYSQL_PORT", "3306"),
		MySQLDatabase:           env("MYSQL_DATABASE", "rollin"),
		MySQLUser:               env("MYSQL_USER", "rollin"),
		MySQLPassword:           os.Getenv("MYSQL_PASSWORD"),
		MySQLAutoCreateDatabase: env("MYSQL_AUTO_CREATE_DATABASE", "true") == "true",
		RedisHost:               env("REDIS_HOST", "127.0.0.1"),
		RedisPort:               env("REDIS_PORT", "6379"),
		RedisPassword:           os.Getenv("REDIS_PASSWORD"),
		SessionTTL:              24 * time.Hour,
		CookieSecure:            env("COOKIE_SECURE", "true") == "true",
		SuperAdminName:          env("SUPER_ADMIN_NAME", "超级管理员"),
		SuperAdminEmail:         strings.ToLower(strings.TrimSpace(os.Getenv("SUPER_ADMIN_EMAIL"))),
		// SUPER_ADMIN_INITIAL_PASSWORD is the canonical name; SUPER_ADMIN_PASSWORD is
		// accepted as a legacy alias so existing deployments keep booting.
		SuperAdminInitialPassword: env("SUPER_ADMIN_INITIAL_PASSWORD", os.Getenv("SUPER_ADMIN_PASSWORD")),
		MailWorkerEvery:           15 * time.Second,
		RefillWorkerEvery:         15 * time.Second,
	}
	// Single-domain deployments serve the console from the same host as the offers.
	cfg.CandidateBaseURL = strings.TrimRight(env("CANDIDATE_BASE_URL", env("PUBLIC_BASE_URL", "https://t.example.edu.cn")), "/")
	cfg.AdminBaseURL = strings.TrimRight(env("ADMIN_BASE_URL", cfg.CandidateBaseURL), "/")
	if value := os.Getenv("REDIS_DB"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return Config{}, errors.New("REDIS_DB 必须是非负整数")
		}
		cfg.RedisDB = parsed
	}
	if value := os.Getenv("SESSION_HOURS"); value != "" {
		hours, err := strconv.Atoi(value)
		if err != nil || hours < 1 {
			return Config{}, errors.New("SESSION_HOURS 必须是正整数")
		}
		cfg.SessionTTL = time.Duration(hours) * time.Hour
	}
	cfg.CSRFAllowedOrigins = splitList(os.Getenv("CSRF_ALLOWED_ORIGINS"))

	// Mail worker SMTP send timeout (P3-7); zero/invalid values keep the 30s default.
	cfg.MailSendTimeout = 30 * time.Second
	if value := os.Getenv("MAIL_SMTP_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.MailSendTimeout = time.Duration(parsed) * time.Second
		}
	}

	// Login throttle thresholds are configurable; zero/invalid values fall back to the
	// contractual 5-per-15-minutes (04 §1.4).
	cfg.LoginRateLimit = 5
	cfg.LoginRateWindow = 15 * time.Minute
	if value := os.Getenv("LOGIN_RATE_LIMIT"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.LoginRateLimit = parsed
		}
	}
	if value := os.Getenv("LOGIN_RATE_WINDOW_MINUTES"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.LoginRateWindow = time.Duration(parsed) * time.Minute
		}
	}

	var missing []string
	if cfg.MySQLHost == "" || cfg.MySQLPort == "" || cfg.MySQLDatabase == "" || cfg.MySQLUser == "" {
		missing = append(missing, "MYSQL_HOST/MYSQL_PORT/MYSQL_DATABASE/MYSQL_USER")
	}
	if cfg.SuperAdminEmail == "" {
		missing = append(missing, "SUPER_ADMIN_EMAIL")
	}
	if cfg.SuperAdminInitialPassword == "" {
		missing = append(missing, "SUPER_ADMIN_INITIAL_PASSWORD")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("缺少必需的环境变量：%s", strings.Join(missing, ", "))
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("SMTP_ENC_KEY"))
	if err != nil || len(key) != 32 {
		return Config{}, errors.New("SMTP_ENC_KEY 必须是 base64 编码的 32 字节密钥（AES-256-GCM，用于 SMTP 密码加密）")
	}
	cfg.SMTPEncKey = key
	return cfg, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
