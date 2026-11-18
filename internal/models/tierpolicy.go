package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"

	"github.com/BurntSushi/toml"
)

// A workspace can forbid Ultrafast independently of its model catalog.
// The catalog ETag doesn't cover that policy. For managed or unknown plans
// fetch the catalog again after expiry rather than reusing a filtered 304.
type codexTierSource struct {
	httpSource

	personal  bool
	policyURL string
}

func (s codexTierSource) Fetch(ctx context.Context, etag string) (Fetched, error) {
	f, err := s.httpSource.Fetch(ctx, etag)
	if err != nil || s.personal {
		return f, err
	}
	for _, m := range f.Models {
		if !slices.Contains(m.ServiceTiers, "ultrafast") {
			continue
		}
		f.ETag = ""
		if !s.allowsUltrafast(ctx) {
			for i := range f.Models {
				m := &f.Models[i]
				m.ServiceTiers = slices.DeleteFunc(m.ServiceTiers, func(t string) bool { return t == "ultrafast" })
				if m.DefaultServiceTier == "ultrafast" {
					m.DefaultServiceTier = ""
				}
			}
		}

		break
	}

	return f, nil
}

func (s codexTierSource) allowsUltrafast(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.policyURL, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var bundle struct {
		Requirements struct {
			Managed []struct{ ID, Contents string } `json:"enterprise_managed"`
		} `json:"requirements_toml"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&bundle); err != nil {
		return false
	}
	for _, f := range bundle.Requirements.Managed {
		var rules struct{ Features map[string]bool }
		if _, err := toml.Decode(f.Contents, &rules); err != nil {
			return false
		}
		if on, set := rules.Features["ultrafast_mode"]; set && !on {
			return false
		}
		if on, set := rules.Features["fast_mode"]; f.ID == "rbac-fast-mode" && set && !on {
			return false
		}
	}

	return true
}
