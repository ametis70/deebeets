package deezer

import (
	"errors"
	"net"
	"strings"
)

// ErrRateLimited is returned when Deezer signals throttling (HTTP 429 or a
// quota gateway error). The pipeline treats it as a global pause — not a
// per-item failure — to avoid an account ban.
var ErrRateLimited = errors.New("deezer: rate limited")

// classifyErr maps transport errors to ErrRateLimited where appropriate,
// otherwise returns the original error.
func classifyErr(err error) error {
	if err == nil {
		return nil
	}
	// Repeated connection resets/timeouts under load often precede a hard block;
	// surface timeouts as-is (retryable) but leave net errors to the caller.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return err
	}
	return err
}

// isRateLimitErr reports whether a gw error payload indicates throttling.
func isRateLimitErr(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "too many requests") ||
		strings.Contains(l, "quota") ||
		strings.Contains(l, "ratelimit") ||
		strings.Contains(l, "rate limit")
}

// IsRateLimited reports whether err is (or wraps) ErrRateLimited.
func IsRateLimited(err error) bool {
	return errors.Is(err, ErrRateLimited)
}
