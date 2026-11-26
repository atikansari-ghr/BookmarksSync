package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"time"
)

// Limits configures the password attempt limiter. Zero fields take the defaults.
type Limits struct {
	// MaxKeys caps the number of tracked keys (username+peer pairs plus peers) in memory.
	MaxKeys int
	// Window is the fixed window, started by a key's first failure, over which
	// failures are counted.
	Window time.Duration
	// UserFailures is the number of failures plus in-flight attempts allowed per window
	// for one normalized username from one direct peer. Failures against a username
	// from one address never block the same username from another address.
	UserFailures int
	// PeerFailures is the number of failures plus in-flight attempts allowed per window
	// for one direct peer address, across all usernames.
	PeerFailures int
}

// DefaultLimits are used for any zero field of Options.Limits.
var DefaultLimits = Limits{MaxKeys: 10000, Window: 15 * time.Minute, UserFailures: 10, PeerFailures: 100}

// inFlightRetry is the Retry-After for attempts refused only because capacity is held
// by attempts still being verified, or because the table is full of them.
const inFlightRetry = time.Second

func (l Limits) withDefaults() Limits {
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultLimits.MaxKeys
	}
	l.MaxKeys = max(l.MaxKeys, 2) // one reservation holds two keys
	if l.Window <= 0 {
		l.Window = DefaultLimits.Window
	}
	if l.UserFailures <= 0 {
		l.UserFailures = DefaultLimits.UserFailures
	}
	if l.PeerFailures <= 0 {
		l.PeerFailures = DefaultLimits.PeerFailures
	}
	return l
}

type bucket struct {
	start    time.Time // start of the failure window; meaningful while count > 0
	count    int       // failures recorded in the window
	inFlight int       // reservations not yet finalized; such a bucket is never removed
	seq      uint64    // last update order, for least-recently-updated eviction
}

// Limiter bounds password attempts per (normalized username, direct peer) pair and per
// direct peer, in fixed windows. An attempt must Reserve capacity before verifying a
// password; the reservation counts against both keys until it is finalized, so
// concurrent attempts cannot all pass before earlier failures are recorded.
//
// Storage is bounded by MaxKeys. Keys with reservations in flight are never pruned or
// evicted; otherwise expired keys are pruned and then the least recently updated key
// is evicted. If every tracked key is in flight, attempts needing a new key are
// refused. The peer is the connection's remote address; forwarded headers are never
// read.
type Limiter struct {
	mu     sync.Mutex
	limits Limits
	now    func() time.Time
	keys   map[string]*bucket
	seq    uint64
}

// Reservation is capacity held for one password attempt. Exactly one of Success, Fail
// or Cancel takes effect; later calls are no-ops.
type Reservation struct {
	l    *Limiter
	keys [2]string
	done bool // guarded by l.mu
}

// NewLimiter returns a limiter using now as its clock (time.Now when nil).
func NewLimiter(l Limits, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	l = l.withDefaults()
	return &Limiter{limits: l, now: now, keys: make(map[string]*bucket, min(l.MaxKeys, 1024))}
}

// pairKey is a fixed-size key for a normalized (trimmed, lower-cased) username at a
// direct peer, whatever the submitted length.
func pairKey(username, peer string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(username))))
	h.Write([]byte{0})
	h.Write([]byte(peer))
	return "u:" + hex.EncodeToString(h.Sum(nil)[:16])
}

// peerKey is the direct peer IP from a RemoteAddr; IPv6 peers are grouped by /64,
// the usual allocation to a single host or site.
func peerKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		if len(host) > 64 {
			host = host[:64]
		}
		return "p:" + host
	case ip.To4() != nil:
		return "p:" + ip.To4().String()
	default:
		return "p:" + ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
}

// failures is the bucket's failure count in its current window.
func (l *Limiter) failures(b *bucket, now time.Time) int {
	if b == nil || b.count == 0 || !now.Before(b.start.Add(l.limits.Window)) {
		return 0
	}
	return b.count
}

// Reserve atomically checks and holds capacity for an attempt for username from
// remoteAddr. It returns nil and how long to wait when the attempt must be refused.
func (l *Limiter) Reserve(username, remoteAddr string) (*Reservation, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	peer := peerKey(remoteAddr)
	keys := [2]string{pairKey(username, peer), peer}
	maxes := [2]int{l.limits.UserFailures, l.limits.PeerFailures}

	var wait time.Duration
	missing := 0
	for i, k := range keys {
		b := l.keys[k]
		if b == nil {
			missing++
			continue
		}
		f := l.failures(b, now)
		if f+b.inFlight < maxes[i] {
			continue
		}
		d := inFlightRetry
		if f >= maxes[i] {
			d = b.start.Add(l.limits.Window).Sub(now)
		}
		wait = max(wait, d)
	}
	if wait > 0 {
		return nil, wait
	}
	if !l.makeRoom(now, missing, keys) {
		return nil, inFlightRetry
	}
	for _, k := range keys {
		b := l.keys[k]
		if b == nil {
			b = &bucket{}
			l.keys[k] = b
		}
		l.seq++
		b.inFlight++
		b.seq = l.seq
	}
	return &Reservation{l: l, keys: keys}, 0
}

// Success finalizes an attempt whose password was verified; it is not a failure.
func (r *Reservation) Success() { r.finish(false) }

// Fail finalizes an attempt whose password was rejected, recording a failure.
func (r *Reservation) Fail() { r.finish(true) }

// Cancel releases the capacity without recording an outcome, for attempts that ended
// without a verification result (internal errors).
func (r *Reservation) Cancel() { r.finish(false) }

func (r *Reservation) finish(failed bool) {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	now := l.now()
	for _, k := range r.keys {
		b := l.keys[k]
		if b == nil {
			continue // unreachable: buckets in flight are never removed
		}
		b.inFlight--
		if failed {
			if l.failures(b, now) == 0 {
				b.start, b.count = now, 0
			}
			b.count++
			l.seq++
			b.seq = l.seq
		}
		if b.inFlight == 0 && l.failures(b, now) == 0 {
			delete(l.keys, k)
		}
	}
}

// makeRoom ensures n more keys fit under MaxKeys without removing any bucket that has
// reservations in flight or is one of protect. It reports whether it succeeded.
func (l *Limiter) makeRoom(now time.Time, n int, protect [2]string) bool {
	if n == 0 || len(l.keys)+n <= l.limits.MaxKeys {
		return true
	}
	removable := func(k string, b *bucket) bool {
		return b.inFlight == 0 && k != protect[0] && k != protect[1]
	}
	for k, b := range l.keys {
		if removable(k, b) && l.failures(b, now) == 0 {
			delete(l.keys, k)
		}
	}
	for len(l.keys)+n > l.limits.MaxKeys {
		var oldest string
		var oldestSeq uint64
		for k, b := range l.keys {
			if removable(k, b) && (oldest == "" || b.seq < oldestSeq) {
				oldest, oldestSeq = k, b.seq
			}
		}
		if oldest == "" {
			return false
		}
		delete(l.keys, oldest)
	}
	return true
}

// Len reports the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
