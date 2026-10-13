package embedded

// BridgeToken is a session's tool bridge token, which only its commands
// get, for the tests that call the bridge directly.
func BridgeToken(e *Engine, sessionID string) string {
	e.bridges.mu.Lock()
	defer e.bridges.mu.Unlock()
	if br, ok := e.bridges.sessions[sessionID]; ok {
		return br.token
	}

	return ""
}
