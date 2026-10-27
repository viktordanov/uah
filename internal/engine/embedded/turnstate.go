package embedded

import (
	"net/http"
	"sync/atomic"
)

// headerTurnState is the ChatGPT backend's sticky-routing token.
const headerTurnState = "X-Codex-Turn-State"

// turnStateTransport replays the backend's x-codex-turn-state within a
// turn, as Codex does (core/src/client.rs: the first value a response of
// the turn carries goes with each later request of the turn, and a new
// turn starts without one). A run is the turn: its clients, one per
// variant, share the state wiring.client makes for the run.
type turnStateTransport struct {
	base  http.RoundTripper
	state *atomic.Pointer[string]
}

func (t turnStateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if v := t.state.Load(); v != nil {
		req = req.Clone(req.Context())
		req.Header.Set(headerTurnState, *v)
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		if v := resp.Header.Get(headerTurnState); v != "" {
			t.state.CompareAndSwap(nil, &v)
		}
	}

	return resp, err // a transport returns its base's errors unchanged
}
