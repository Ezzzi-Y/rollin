package mail

// sendPacer is the per-SMTP-host send interval register (发件服务器限流保护).
//
// Providers such as smtp.qq.com / smtp.163.com throttle accounts that submit too
// fast, so the worker rests a fixed interval between two submissions to the SAME
// host; each host keeps its own clock (每个服务器有自己的间隔). The register is
// process-local by design: the mail worker is a single goroutine, so
// ReadyAt → send → Record is naturally serialized and needs no locking discipline
// beyond the mutex. In a multi-instance deployment each process paces its own
// submissions (the effective global rate is then instances × 1/interval).
//
// A host that is still resting never holds a claimed task: the worker puts the task
// back to PENDING with next_retry_at = the host's next allowed send time
// (DeferTask, no retry_count bump), so tasks for OTHER hosts keep draining during
// the rest and the queue never blocks on a timer.

import (
	"strings"
	"sync"
	"time"
)

type sendPacer struct {
	mu       sync.Mutex
	interval time.Duration // <= 0 disables pacing entirely
	last     map[string]time.Time
}

func newSendPacer(interval time.Duration) *sendPacer {
	return &sendPacer{interval: interval, last: make(map[string]time.Time)}
}

// ReadyAt reports when the next submission to host may start: ok=true means "now".
// It never mutates the register — Record does, right after the send attempt, so the
// rest counts from the moment the previous submission FINISHED (发完休息一分钟).
func (p *sendPacer) ReadyAt(host string, now time.Time) (time.Time, bool) {
	host = normalizeHost(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.interval <= 0 {
		return time.Time{}, true
	}
	last, ok := p.last[host]
	if !ok {
		return time.Time{}, true
	}
	ready := last.Add(p.interval)
	if !now.Before(ready) {
		return time.Time{}, true
	}
	return ready, false
}

// Record stamps one finished submission. It is called for failures too: a failed
// attempt contacted the server just the same and counts toward provider throttling.
func (p *sendPacer) Record(host string, now time.Time) {
	host = normalizeHost(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last[host] = now
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}
