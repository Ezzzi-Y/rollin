// Package ratelimit implements the Redis fixed-window counters of 03-permissions.md §4.4:
// login throttling (IP+email, 5 per 15 minutes → RATE_LIMITED) today, and the same
// primitive backs the public-offer and import token limits for P4/P5.
//
// The window is a plain INCR + EXPIRE on a bucket key — good enough for the campus scale
// of D6 and atomic enough because INCR is single-threaded in Redis. Redis outages fail
// OPEN (the limiter returns allowed=true): availability of the login path beats strict
// throttling, and sessions live in Redis anyway so a down Redis breaks the app regardless.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces all buckets so a flush of another subsystem can never collide.
const keyPrefix = "rollin:ratelimit:"

// Limiter is the fixed-window counter service.
type Limiter struct {
	rdb    *redis.Client
	logger *slog.Logger
}

func New(rdb *redis.Client, logger *slog.Logger) *Limiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Limiter{rdb: rdb, logger: logger}
}

// Key builds the per-bucket counter key. Sensitive key parts (emails, tokens) are hashed
// so raw credentials never appear in Redis keys or dumps.
func Key(bucket, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return keyPrefix + bucket + ":" + hex.EncodeToString(sum[:8])
}

// Allow consumes one unit from the bucket identified by (bucket, identity) and reports
// whether the caller is still under limit. A fresh window starts on the first hit and
// expires after `window`; counts inside one window are capped at `limit`.
func (l *Limiter) Allow(ctx context.Context, bucket, identity string, limit int, window time.Duration) (bool, error) {
	key := Key(bucket, identity)
	count, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		// Fail open on Redis errors (see package doc) but make the degradation visible.
		l.logger.Warn("ratelimit: redis unavailable, failing open", "bucket", bucket, "error", err)
		return true, err
	}
	if count == 1 {
		// First hit of the window: set the TTL. A race with a concurrent INCR can
		// briefly extend the window; harmless for a throttle (never shortens it).
		l.rdb.Expire(ctx, key, window)
	}
	if count > int64(limit) {
		return false, nil
	}
	return true, nil
}

// LoginBuckets names the two login throttle buckets (03 §4.4: extend the legacy IP+email
// limiter to both scopes).
const (
	BucketPlatformLogin = "login:platform"
	BucketActivityLogin = "login:activity"
)

// LoginIdentity composes the IP+email throttle identity of 04-api-contract.md §1.4.
func LoginIdentity(ip, email string) string {
	return fmt.Sprintf("%s|%s", ip, email)
}
