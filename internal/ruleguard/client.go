// Package ruleguard enforces repository/ruleset rules at the executor — the
// single point where every push (ssh or http) lands before refs are updated.
//
// It calls the orchestrator (BFF) rules-validate endpoint, mirroring what the
// ssh/http gateways did previously. Consolidating the call here makes the
// executor the one choke point: no ref update bypasses validation regardless of
// transport, and the rule is implemented once instead of per-gateway.
//
// Auth: the inbound push request already carries the orchestrator-minted
// decision token (the executor verifies it via DecisionJWTConfig). We forward
// that same token on the outbound validate call — the executor needs no service
// credential of its own. The acting principal is passed as X-User-ID.
package ruleguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls the orchestrator (BFF) rules-validate endpoint.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a ruleguard client targeting the orchestrator base URL.
// A zero baseURL disables enforcement (Validate becomes a no-op) so the
// executor degrades open rather than blocking all pushes on misconfiguration.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// PushTarget identifies one ref update to validate.
type PushTarget struct {
	RepositoryID  string // resolved numeric repository id (from the gateway)
	WorkspaceSlug string // workspace slug (from the gateway), for X-Workspace-Slug
	Branch        string // short branch name, e.g. "main" (parsed from the push)
	UserID        string // acting principal (X-User-ID, forwarded by the gateway)
	DecisionToken string // orchestrator decision JWT forwarded from the inbound request
}

// validateRequest is the JSON body for POST .../rules/validate. Shape matches
// the gateways' previous ValidatePushRules request (api-service contract).
type validateRequest struct {
	Action      string         `json:"action"`
	TargetType  string         `json:"target_type"`
	TargetValue string         `json:"target_value"`
	Context     map[string]any `json:"context"`
}

type validateResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Denied is returned when a push violates a rule. Reason is safe to surface to
// the git client (sideband).
type Denied struct {
	Branch string
	Reason string
}

func (d *Denied) Error() string {
	if d.Reason != "" {
		return d.Reason
	}
	return fmt.Sprintf("push to %s rejected by ruleset", d.Branch)
}

// ValidatePush checks one ref update against the repository's rules/rulesets.
//
// Returns:
//   - nil                         → allowed (also the no-op case: missing
//     baseURL / decision token / repository id — degrade open, the gateways
//     still validate during dual-run).
//   - *Denied                     → rule violation (reject the push).
//   - other error                 → transport/API failure (caller decides
//     fail-open vs fail-closed).
func (c *Client) ValidatePush(ctx context.Context, t PushTarget) error {
	// Degrade open on incomplete context so a config/propagation gap never
	// blocks every push (dual-run: gateways still enforce meanwhile).
	if c.baseURL == "" || t.RepositoryID == "" || t.DecisionToken == "" {
		return nil
	}

	url := fmt.Sprintf("%s/api/v2/repositories/%s/rules/validate", c.baseURL, t.RepositoryID)
	body := validateRequest{
		Action:      "push",
		TargetType:  "branch",
		TargetValue: t.Branch,
		Context: map[string]any{
			"action": "push",
			"branch": map[string]string{"name": t.Branch},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ruleguard marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("ruleguard request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.DecisionToken)
	req.Header.Set("Content-Type", "application/json")
	if t.UserID != "" {
		req.Header.Set("X-User-ID", t.UserID)
	}
	if t.WorkspaceSlug != "" {
		req.Header.Set("X-Workspace-Slug", t.WorkspaceSlug)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ruleguard call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		// Body, when present, carries {allowed, reason}. Treat an explicit
		// allowed=false as a denial even on 200 (defensive — the endpoint
		// today signals denial via status, but honor the field too).
		if len(bytes.TrimSpace(respBody)) > 0 {
			var vr validateResponse
			if err := json.Unmarshal(respBody, &vr); err == nil && !vr.Allowed {
				return &Denied{Branch: t.Branch, Reason: vr.Reason}
			}
		}
		return nil
	case http.StatusUnprocessableEntity, http.StatusForbidden, http.StatusConflict:
		// Rule violation. Pull a human reason out of the body if present.
		return &Denied{Branch: t.Branch, Reason: reasonFromBody(respBody, t.Branch)}
	default:
		return fmt.Errorf("ruleguard validate: status %d, body: %s", resp.StatusCode, truncate(respBody, 200))
	}
}

func reasonFromBody(body []byte, branch string) string {
	var vr validateResponse
	if err := json.Unmarshal(body, &vr); err == nil && vr.Reason != "" {
		return vr.Reason
	}
	// api-service error envelope: {"error":{"title":"..."}}
	var env struct {
		Error struct {
			Title string `json:"title"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Title != "" {
		return env.Error.Title
	}
	return fmt.Sprintf("push to %s rejected by ruleset", branch)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
