package downloader

import (
	"sync"
	"time"
)

// rateGate coordinates the download stage's response to Deezer throttling. On a
// hit it opens (blocks dispatch) for an exponentially growing cooldown; too many
// hits within the window trip a hard stop to protect the account.
type rateGate struct {
	cooldown time.Duration
	maxHits  int
	window   time.Duration

	mu        sync.Mutex
	openUntil time.Time
	hits      []time.Time
	streak    int
	hardStop  bool
}

func newRateGate(cooldown, window time.Duration, maxHits int) *rateGate {
	return &rateGate{cooldown: cooldown, maxHits: maxHits, window: window}
}

// hit records a rate-limit event and returns how long to wait and whether the
// stage should hard-stop.
func (g *rateGate) hit() (wait time.Duration, hardStop bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	// Drop hits outside the sliding window.
	cutoff := now.Add(-g.window)
	kept := g.hits[:0]
	for _, t := range g.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	g.hits = append(kept, now)

	g.streak++
	// Exponential backoff: cooldown * 2^(streak-1), capped at 32x.
	mult := 1 << min(g.streak-1, 5)
	wait = g.cooldown * time.Duration(mult)
	g.openUntil = now.Add(wait)

	if g.maxHits > 0 && len(g.hits) >= g.maxHits {
		g.hardStop = true
	}
	return wait, g.hardStop
}

// clear resets the backoff streak after a successful download.
func (g *rateGate) clear() {
	g.mu.Lock()
	g.streak = 0
	g.mu.Unlock()
}

// blockedFor returns the remaining cooldown (0 if open) and the hard-stop flag.
func (g *rateGate) blockedFor() (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hardStop {
		return 0, true
	}
	if d := time.Until(g.openUntil); d > 0 {
		return d, false
	}
	return 0, false
}

// retryable reports whether another attempt should be made given the retry mode
// and how many attempts have already happened.
func shouldRetryImmediate(mode string, attempts, maxAttempts int) bool {
	if mode != "immediate" && mode != "both" {
		return false
	}
	return attempts < maxAttempts
}

func deferredEnabled(mode string) bool {
	return mode == "deferred" || mode == "both"
}
