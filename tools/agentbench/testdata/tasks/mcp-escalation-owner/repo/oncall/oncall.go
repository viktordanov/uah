// Package oncall says whom to page for memory leaks.
package oncall

// Service is the service whose memory leaks we escalate, and Owner its
// owner's address.
const (
	Service = "payments"
	Owner   = "maya.ortiz@example.com"
)

// Page is who gets paged for a memory leak.
func Page() string { return Owner + " (" + Service + ")" }
