// Package codexauth serves the ChatGPT subscription credentials the
// openai-codex provider sends: the access token and the account ID, from the
// environment or from Codex's auth file, which it refreshes as Codex does
// (login.go, refresh.go, and docs/design/codex-auth.md).
//
// Load and its checks are adapted from unreal-agent-runner v0.1.1,
// harness/llm/clients/openaicodex/credentials.go (MIT License, Copyright (c)
// 2026 Unreal Labs). The package keeps these helpers private, and the
// priority client and the model catalog need the same credentials.
package codexauth

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm/clients/openaicodex"
)

// Creds are the credentials for the ChatGPT backend. Never log them.
type Creds struct {
	AccessToken string
	AccountID   string
}

// Load reads the credentials the config selects (the environment's token or
// the Codex auth file) and checks them as the runner's client does. It never
// refreshes: an expired token is an error. Errors never include a secret.
func Load(config openaicodex.Config) (Creds, error) {
	token, accountID := strings.TrimSpace(config.AccessToken), strings.TrimSpace(config.AccountID)
	if config.AuthFile != "" {
		if token != "" || accountID != "" {
			return Creds{}, errors.New("codex AuthFile cannot be combined with AccessToken or AccountID")
		}
		auth, _, err := readAuthFile(config.AuthFile)
		if err != nil {
			return Creds{}, err
		}
		token, accountID = auth.token, auth.accountID
	}
	creds, expires, err := check(token, accountID)
	if err != nil {
		return Creds{}, err
	}
	if expired(expires, time.Now()) {
		return Creds{}, errors.New("codex access token has expired; renew credentials externally")
	}

	return creds, nil
}

// check checks a token and account as the runner's client does, except the
// expiry, which it returns (0 when the token has none).
func check(token, accountID string) (Creds, int64, error) {
	if token == "" {
		return Creds{}, 0, errors.New("codex access token must be set; use OPENAI_CODEX_ACCESS_TOKEN or a ChatGPT-authenticated Codex auth file")
	}
	if strings.HasPrefix(token, "sk-") || !HeaderValue(token) {
		return Creds{}, 0, errors.New("codex requires a subscription access token, not an API key or invalid header value")
	}
	claimAccount, expires, err := codexTokenClaims(token)
	if err != nil {
		return Creds{}, 0, err
	}
	if accountID == "" {
		accountID = claimAccount
	} else if claimAccount != "" && accountID != claimAccount {
		return Creds{}, 0, errors.New("codex account ID does not match the access token")
	}
	if accountID == "" || !HeaderValue(accountID) {
		return Creds{}, 0, errors.New("codex account ID must be set in OPENAI_CODEX_ACCOUNT_ID, the auth file, or the access token")
	}

	return Creds{AccessToken: token, AccountID: accountID}, expires, nil
}

// expired reports whether a token with this expiry (0: none) has expired.
func expired(expires int64, now time.Time) bool { return expires != 0 && now.Unix() >= expires }

// codexTokenClaims reads unverified routing and expiry hints; the server
// authenticates the token.
func codexTokenClaims(token string) (accountID string, expires int64, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, errors.New("invalid Codex access token claims")
	}
	var claims struct {
		Expires int64 `json:"exp"`
		Auth    struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", 0, errors.New("invalid Codex access token claims")
	}

	return claims.Auth.AccountID, claims.Expires, nil
}

// HeaderValue reports whether value can be sent as an HTTP header value.
func HeaderValue(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r > '~' })
}

// AccountKind describes known Codex plan families without guessing for unknown SKUs.
func AccountKind(plan string) string {
	switch plan {
	case "free", "go", "plus", "pro", "prolite", "promax":
		return "personal"
	case "business", "enterprise", "edu", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "hc", "education", "edu_plus", "edu_pro", "sci", "sci_plus":
		return "workspace"
	}

	return ""
}

// PlanHint is account metadata, not a model entitlement.
func (c Creds) PlanHint() string {
	p := strings.Split(c.AccessToken, ".")
	if len(p) != 3 {
		return ""
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return ""
	}
	var v struct {
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}

	return v.Auth.Plan
}
