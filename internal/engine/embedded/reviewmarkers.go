package embedded

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// reviewMarkers annotates auto-review on every provider. Codex billing
// classification is separately opt-in and remains the backend's decision.
func (c *modelCall) reviewMarkers(body []byte, header http.Header) ([]byte, error) {
	if c.kind != kindReview && !c.guardianMarkers {
		return body, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("failed to decode review request: %w", err)
	}
	metadata := map[string]json.RawMessage{}
	if raw := fields["client_metadata"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, fmt.Errorf("failed to decode client metadata: %w", err)
		}
	}
	if c.kind == kindReview {
		delete(fields, "service_tier")
		header.Set("X-Openai-Subagent", "guardian")
		metadata["x-openai-subagent"] = json.RawMessage(`"guardian"`)
		if c.guardianMarkers {
			header.Set("X-Codex-Guardian", "reviewer")
			if c.parentResponse != "" {
				metadata["parent_response_id"], _ = json.Marshal(c.parentResponse) //nolint:errchkjson // a string always encodes
			}
		}
	} else {
		metadata["guardian_credits_requested"] = json.RawMessage(`"true"`)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to encode client metadata: %w", err)
	}
	fields["client_metadata"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("failed to encode review request: %w", err)
	}

	return out, nil
}

func guardianMarkers(provider string, enabled bool) bool {
	return enabled && provider == "openai-codex"
}
