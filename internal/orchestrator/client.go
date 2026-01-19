package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL            string
	sshKeyValidatePath string
	httpClient         *http.Client
	forwardAuthHeader  bool
}

type SSHKeyValidationResponse struct {
	UserID string `json:"user_id"`
}

func NewClient(baseURL, sshKeyValidatePath string, timeout time.Duration, forwardAuthHeader bool) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("orchestrator baseURL is required")
	}
	if strings.TrimSpace(sshKeyValidatePath) == "" {
		return nil, fmt.Errorf("orchestrator sshKeyValidatePath is required")
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	return &Client{
		baseURL:            strings.TrimRight(baseURL, "/"),
		sshKeyValidatePath: sshKeyValidatePath,
		httpClient:         &http.Client{Timeout: timeout},
		forwardAuthHeader:  forwardAuthHeader,
	}, nil
}

// ResolveUserIDFromSSHKey calls orchestrator ssh-key validation endpoint and returns user_id.
//
// NOTE: endpoint shape is configurable; we send standard query params:
// - fingerprint
// - repo_path (e.g. "owner/repo")
func (c *Client) ResolveUserIDFromSSHKey(ctx context.Context, fingerprint, repoPath, authHeader string) (string, error) {
	if strings.TrimSpace(fingerprint) == "" {
		return "", fmt.Errorf("ssh key fingerprint is required")
	}
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repo_path is required")
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid orchestrator base url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + c.sshKeyValidatePath
	q := u.Query()
	q.Set("fingerprint", fingerprint)
	q.Set("repo_path", repoPath)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create orchestrator request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.forwardAuthHeader && strings.TrimSpace(authHeader) != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call orchestrator: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("orchestrator returned non-2xx status: %d", resp.StatusCode)
	}

	var parsed SSHKeyValidationResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("failed to decode orchestrator response: %w", err)
	}
	if strings.TrimSpace(parsed.UserID) == "" {
		return "", fmt.Errorf("orchestrator response missing user_id")
	}
	return parsed.UserID, nil
}

