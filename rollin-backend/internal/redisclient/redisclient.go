// Package redisclient wraps the single Redis client used by Rollin. Redis currently backs
// three things only — platform/activity sessions, the distributed candidate accept lock
// and worker leases (P2/P5 consumers). This package owns connection setup and the
// compare-and-release lease lock so domain code never hand-rolls Lua.
package redisclient

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// Options mirrors the Redis-related environment values.
type Options struct {
	Addr     string // host:port
	Password string
	DB       int
}

// New builds and pings a client. Callers own Close.
func New(ctx context.Context, opts Options) (*redis.Client, error) {
	if opts.Addr == "" {
		opts.Addr = net.JoinHostPort("127.0.0.1", "6379")
	}
	client := redis.NewClient(&redis.Options{Addr: opts.Addr, Password: opts.Password, DB: opts.DB})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return client, nil
}

// ErrLockHeld is returned when a lease lock is already held by someone else.
var ErrLockHeld = errors.New("redisclient: lock held elsewhere")

// Locker implements the Redis lease lock used for candidate accepts and activity-scoped
// critical sections: SET NX PX to acquire, owner-token-checked release and renewal.
// Every mutation is compare-and-set via Lua so a stale holder can never release or
// renew a lease that has been taken over.
type Locker struct {
	client *redis.Client
}

func NewLocker(client *redis.Client) *Locker { return &Locker{client: client} }

// Key builders keep lock naming consistent across phases.
func CandidateAcceptLockKey(candidateID uint64) string {
	return "rollin:lock:candidate:" + itoa(candidateID) + ":accept-offer"
}

func ActivityLockKey(activityID uint64) string {
	return "rollin:lock:activity:" + itoa(activityID)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// Acquire tries to take the lock for ttl. It returns the owner token (needed by
// Release/Renew) and whether acquisition succeeded.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	owner, err := randomToken()
	if err != nil {
		return "", false, err
	}
	ok, err := l.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return "", false, err
	}
	return owner, ok, nil
}

// Release drops the lock only if the caller still owns it.
func (l *Locker) Release(ctx context.Context, key, owner string) error {
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	res, err := l.client.Eval(ctx, script, []string{key}, owner).Result()
	if err != nil {
		return err
	}
	if affected, _ := res.(int64); affected != 1 {
		return ErrLockHeld
	}
	return nil
}

// Renew extends the lease only if the caller still owns it (worker heartbeat).
func (l *Locker) Renew(ctx context.Context, key, owner string, ttl time.Duration) error {
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) else return 0 end`
	res, err := l.client.Eval(ctx, script, []string{key}, owner, ttl.Milliseconds()).Result()
	if err != nil {
		return err
	}
	if affected, _ := res.(int64); affected != 1 {
		return ErrLockHeld
	}
	return nil
}
