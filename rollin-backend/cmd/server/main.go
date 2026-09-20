// Command rollin-server is the Rollin V1 backend entry point.
//
// Modes:
//   - `rollin-server`                  — run the HTTP server (read-only schema gate).
//   - `rollin-server migrate up`       — apply pending migrations (stop on first error).
//   - `rollin-server migrate status`   — show applied/pending versions.
//   - `rollin-server migrate precheck` — run the C1–C10 legacy prechecks, no writes.
//
// The server never mutates the schema itself: it verifies the database is at least
// MinimumSchemaVersion and that recorded checksums match this binary, then boots
// (06-migration.md §1.2).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"
	"rollin-backend/internal/activity"
	"rollin-backend/internal/admission"
	"rollin-backend/internal/application"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/auth"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/config"
	"rollin-backend/internal/dashboard"
	"rollin-backend/internal/db"
	"rollin-backend/internal/db/migrations"
	"rollin-backend/internal/export"
	"rollin-backend/internal/httpapi"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/mail"
	"rollin-backend/internal/mailtoken"
	"rollin-backend/internal/member"
	"rollin-backend/internal/offer"
	"rollin-backend/internal/ranking"
	"rollin-backend/internal/ratelimit"
	"rollin-backend/internal/redisclient"
	"rollin-backend/internal/settings"
	"rollin-backend/internal/smtpconfig"
	"rollin-backend/internal/token"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration", "error", err)
		os.Exit(1)
	}
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "migrate" {
		os.Exit(runMigrate(logger, cfg, args[1:]))
	}
	os.Exit(runServer(logger, cfg))
}

// appliedBy identifies the migrating process for the version ledger.
func appliedBy() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s pid=%d", host, os.Getpid())
}

// openDB connects and pings MySQL with the pool defaults of internal/db.
func openDB(cfg config.Config) (*gorm.DB, func() error, error) {
	handle, err := db.Open(db.Options{
		User:     cfg.MySQLUser,
		Password: cfg.MySQLPassword,
		Host:     cfg.MySQLHost,
		Port:     cfg.MySQLPort,
		Database: cfg.MySQLDatabase,
	})
	if err != nil {
		return nil, nil, err
	}
	sqlDB, err := handle.DB()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("ping database: %w", err)
	}
	return handle, sqlDB.Close, nil
}

// runMigrate executes the `migrate` subcommands and returns the process exit code.
func runMigrate(logger *slog.Logger, cfg config.Config, args []string) int {
	if len(args) == 0 {
		logger.Error("usage: rollin-server migrate up|status|precheck [--backup-path <file>]")
		return 2
	}
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	backupPath := flags.String("backup-path", "", "path to the pre-migration mysqldump backup (required for legacy upgrades)")
	allowNoBackup := flags.Bool("allow-no-backup", false, "explicitly acknowledge a missing backup confirmation (dangerous)")
	if err := flags.Parse(args[1:]); err != nil {
		logger.Error("flag parse", "error", err)
		return 2
	}
	handle, closeDB, err := openDB(cfg)
	if err != nil {
		logger.Error("open database", "error", err)
		return 1
	}
	defer func() { _ = closeDB() }()

	runner := migrations.New(handle, appliedBy(), logger)
	ctx := context.Background()
	switch args[0] {
	case "up":
		reports, err := runner.Up(ctx, migrations.UpOptions{BackupPath: *backupPath, AllowMissingBackup: *allowNoBackup})
		if err != nil {
			logger.Error("migrate up", "error", err)
			return 1
		}
		if len(reports) == 0 {
			logger.Info("migrate up", "result", "schema already up to date")
			return 0
		}
		for _, report := range reports {
			logger.Info("migrate up", "version", report.Version, "name", report.Name,
				"checksum", report.Checksum, "ms", report.ExecutionMS, "steps", strings.Join(report.Steps, ","))
		}
		return 0
	case "status":
		text, err := runner.StatusText(ctx)
		if err != nil {
			logger.Error("migrate status", "error", err)
			return 1
		}
		fmt.Print(text)
		return 0
	case "precheck":
		report, err := runner.Precheck(ctx, migrations.PrecheckOptions{BackupPath: *backupPath, AllowMissingBackup: *allowNoBackup})
		if err != nil {
			logger.Error("migrate precheck", "error", err)
			return 1
		}
		fmt.Print(report.Text())
		if !report.Passed() {
			return 1
		}
		return 0
	default:
		logger.Error("unknown migrate subcommand", "command", args[0])
		return 2
	}
}

// runServer boots the HTTP server after the read-only schema gate.
func runServer(logger *slog.Logger, cfg config.Config) int {
	handle, closeDB, err := openDB(cfg)
	if err != nil {
		logger.Error("open database", "error", err)
		return 1
	}
	defer func() { _ = closeDB() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := migrations.New(handle, appliedBy(), logger)
	if err := runner.CheckServerStart(ctx); err != nil {
		logger.Error("schema gate", "error", err)
		return 1
	}

	redisClient, err := redisclient.New(ctx, redisclient.Options{
		Addr:     net.JoinHostPort(cfg.RedisHost, cfg.RedisPort),
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err != nil {
		logger.Error("redis", "error", err)
		return 1
	}
	defer redisClient.Close()

	// Environment values seed the platform parameters; the super admin overrides them at
	// runtime and the store caches them briefly.
	settingStore := settings.NewStore(handle, map[string]string{
		settings.KeySiteName:           "Rollin",
		settings.KeyAdminBaseURL:       cfg.AdminBaseURL,
		settings.KeyPublicBaseURL:      cfg.CandidateBaseURL,
		settings.KeyDefaultOfferMode:   "AUTO",
		settings.KeyDefaultOfferExpire: "72",
		settings.KeyInviteExpireHours:  "72",
		settings.KeySessionHours:       fmt.Sprintf("%d", int(cfg.SessionTTL.Hours())),
	}, 15*time.Second)

	// Domain services. Transaction boundaries live inside each service; audit rows are
	// written in the SAME transaction as the change they describe. Construction order
	// follows the dependency direction: application → ranking → offer → activity.
	auditService := audit.New(handle)
	mailService := mail.New(handle, mail.NewGormRepository(handle), auditService)
	smtpService := smtpconfig.New(handle, smtpconfig.NewGormRepository(handle), cfg.SMTPEncKey, auditService, cfg.MailSendTimeout)
	memberService := member.New(handle, member.NewGormRepository(handle), member.Deps{
		Tokens:   token.New(),
		Mail:     mailService,
		Audit:    auditService,
		SMTP:     smtpService,
		Settings: settingStore,
	})
	// P4: import tokens, single-candidate import and ranking (transaction boundaries in
	// each service; audit rows share the business transaction).
	importTokenService := importtoken.New(handle, token.New(), auditService)
	applicationService := application.New(handle, application.Deps{
		Repo:       application.NewGormRepository(handle),
		Candidates: candidate.NewGormRepository(handle),
		Tokens:     importTokenService,
		Audits:     auditService,
	})
	rankingService := ranking.New(handle, ranking.NewGormRepository(handle), auditService, ranking.Deps{
		Applications: applicationService,
		Candidates:   candidate.NewGormRepository(handle),
		Mail:         mailService,
	})
	// P5: the offer domain. The refill primitive is injected as the narrow Refiller
	// interface so the offer package never imports ranking (which composes offer
	// persistence inside FillByRank). The candidate lease lock is the first of the two
	// INV-2 layers; its unavailability fails the request closed (retryable).
	redisLocker := redisclient.NewLocker(redisClient)
	offerService := offer.New(handle, auditService, offer.Deps{
		MailTokens: mailtoken.New(handle),
		Mail:       mailService,
		SMTP:       smtpService,
		Locker:     redisLocker,
		Refill:     rankingService,
		Logger:     logger,
	})
	activityService := activity.New(handle, activity.NewGormRepository(handle), activity.Deps{
		Audit:    auditService,
		Mail:     mailService,
		Settings: settingStore,
		Offers:   offerService,
		Ranking:  rankingService,
		SMTP:     smtpService,
	})

	// P6: dashboard aggregation and the synchronous XLSX export. Both reuse the shared
	// Offer-caliber functions (offer.Service.Occupied; application.CountByStatus) so the
	// dashboard, the candidate list and the export can never disagree on counts (D6 §3).
	dashboardService := dashboard.New(handle, dashboard.Deps{
		Offers:       offerService,
		Applications: application.NewGormRepository(handle),
	})
	exportService := export.New(export.NewGormRepository(handle))

	// P3: the reliable mail worker drains mail_task with lease claiming, exponential
	// backoff and the pre-send business rechecks (02 §4). It shares the process ctx so
	// SIGINT/SIGTERM stop it with the server. The per-host send rest (发件服务器限流
	// 保护: 同一服务器发完一封休息一分钟再发下一封) is contractual inside the worker.
	mailWorker := mail.NewWorker(mail.WorkerDeps{
		DB:         handle,
		Repo:       mail.NewGormRepository(handle),
		MailTokens: mailtoken.New(handle),
		SMTP:       smtpService,
		Settings:   settingStore,
		Logger:     logger,
	}, mail.DefaultWorkerConfig(cfg.MailWorkerEvery, cfg.MailSendTimeout))
	mailWorker.Start(ctx)

	// P5: the admission background worker — expiry settlement sweeps plus the refill
	// intent executor (D1/D4). It shares the process ctx so SIGINT/SIGTERM stop it
	// with the server.
	admissionWorker := admission.New(admission.Deps{
		DB:      handle,
		Offers:  offerService,
		Ranking: rankingService,
		Locker:  redisLocker,
		Logger:  logger,
	}, admission.Defaults(cfg.RefillWorkerEvery))
	admissionWorker.Start(ctx)

	authService := auth.New(handle, auth.NewSessionStore(redisClient, cfg.SessionTTL), auditService, logger)
	if err := authService.BootstrapSuperAdmin(ctx, cfg.SuperAdminName, cfg.SuperAdminEmail, cfg.SuperAdminInitialPassword); err != nil {
		logger.Error("bootstrap super admin", "error", err)
		return 1
	}

	sessionStore := auth.NewSessionStore(redisClient, cfg.SessionTTL)
	server := &http.Server{
		Addr: cfg.Address,
		Handler: httpapi.New(httpapi.Deps{
			Settings:     settingStore,
			Logger:       logger,
			Auth:         authService,
			Sessions:     sessionStore,
			Activity:     activityService,
			Member:       memberService,
			Audit:        auditService,
			SMTP:         smtpService,
			RateLimiter:  ratelimit.New(redisClient, logger),
			Applications: applicationService,
			Ranking:      rankingService,
			ImportTokens: importTokenService,
			Mail:         mailService,
			Offers:       offerService,
			Dashboard:    dashboardService,
			Export:       exportService,
		}, httpapi.Config{
			CookieSecure:       cfg.CookieSecure,
			CSRFAllowedOrigins: cfg.CSRFAllowedOrigins,
			SessionTTL:         cfg.SessionTTL,
			LoginRateLimit:     cfg.LoginRateLimit,
			LoginRateWindow:    cfg.LoginRateWindow,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("server started", "address", cfg.Address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "error", err)
	}
	return 0
}
