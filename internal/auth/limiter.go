package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"time"
)

// Limits configures the login failure limiter. Zero fields take the defaults.
type Limits struct {
	// MaxKeys caps the number of tracked keys (usernames plus peers) in memory.
	MaxKeys int
	// Window is the fixed window, started by a key's first failure, over which
	// failures are counted.
	Window time.Duration
	// UserFailures and PeerFailures are the failures allowed per window for one
	// normalized username and for one direct peer address respectively.
	UserFailures int
	PeerFailures int
}

// DefaultLimits are used for any zero field of Options.Limits.
var DefaultLimits = Limits{MaxKeys: 10000, Window: 15 * time.Minute, UserFailures: 10, PeerFailures: 100}

func (l Limits) withDefaults() Limits {
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultLimits.MaxKeys
	}
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
	start time.Time
	count int
	seq   uint64 // last update order, for least-recently-updated eviction
}

// Limiter counts failed password attempts per normalized username and per direct
// peer address in fixed windows. Storage is bounded: when MaxKeys keys are tracked,
// expired keys are dropped and then the least recently updated key is evicted. It
// never reads forwarded headers; the peer is the connection's remote address.
type Limiter struct {
	mu     sync.Mutex
	limits Limits
	now    func() time.Time
	keys   map[string]*bucket
	seq    uint64
}

// NewLimiter returns a limiter using now as its clock (time.Now when nil).
func NewLimiter(l Limits, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	l = l.withDefaults()
	return &Limiter{limits: l, now: now, keys: make(map[string]*bucket, min(l.MaxKeys, 1024))}
}

// userKey normalizes a username (trimmed, lower-cased) and hashes it so each key has
// a fixed size whatever the submitted length.
func userKey(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return "u:" + hex.EncodeToString(sum[:16])
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

func (l *Limiter) live(key string, now time.Time) *bucket {
	b := l.keys[key]
	if b == nil || !now.Before(b.start.Add(l.limits.Window)) {
		return nil
	}
	return b
}

// Allow reports whether another attempt for username from remoteAddr may proceed and,
// if not, how long until it may.
func (l *Limiter) Allow(username, remoteAddr string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	check := func(key string, max int) {
		if b := l.live(key, now); b != nil && b.count >= max {
			if d := b.start.Add(l.limits.Window).Sub(now); d > wait {
				wait = d
			}
		}
	}
	check(userKey(username), l.limits.UserFailures)
	check(peerKey(remoteAddr), l.limits.PeerFailures)
	return wait == 0, wait
}

// Fail records a failed attempt for username from remoteAddr.
func (l *Limiter) Fail(username, remoteAddr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range []string{userKey(username), peerKey(remoteAddr)} {
		l.seq++
		if b := l.live(key, now); b != nil {
			b.count++
			b.seq = l.seq
			continue
		}
		if _, tracked := l.keys[key]; !tracked {
			l.makeRoom(now)
		}
		l.keys[key] = &bucket{start: now, count: 1, seq: l.seq}
	}
}

// makeRoom ensures one more key fits under MaxKeys.
func (l *Limiter) makeRoom(now time.Time) {
	if len(l.keys) < l.limits.MaxKeys {
		return
	}
	for k := range l.keys {
		if l.live(k, now) == nil {
			delete(l.keys, k)
		}
	}
	for len(l.keys) >= l.limits.MaxKeys {
		var oldest string
		var oldestSeq uint64
		for k, b := range l.keys {
			if oldest == "" || b.seq < oldestSeq {
				oldest, oldestSeq = k, b.seq
			}
		}
		delete(l.keys, oldest)
	}
}

// Len reports the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
