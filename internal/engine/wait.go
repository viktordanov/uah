package engine

import "time"

// DefaultMaxWait is max_wait's default: how long wait_shell and
// wait_agent wait without a timeout, and the longest timeout they take.
const DefaultMaxWait = time.Hour

// MaxWaitLimit is the longest max_wait.
const MaxWaitLimit = 24 * time.Hour

// WaitTimeout is how long a wait tool waits: maxWait without timeoutMS,
// else timeoutMS within minWait and maxWait. A minWait above maxWait
// yields to it. The number is clamped before it is converted, so a huge
// one cannot overflow.
func WaitTimeout(timeoutMS *float64, minWait, maxWait time.Duration) time.Duration {
	if timeoutMS == nil {
		return maxWait
	}
	ms := min(max(*timeoutMS, float64(min(minWait, maxWait).Milliseconds())), float64(maxWait.Milliseconds()))

	return time.Duration(ms) * time.Millisecond
}
