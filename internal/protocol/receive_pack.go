// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// This file handles receive-pack operations (push) as the executor counterpart to upload-pack (fetch).
package protocol

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
)

// ReceivePackRequest represents a parsed receive-pack request from the client.
// Used by SSH protocol handler.
type ReceivePackRequest struct {
	Commands     []RefUpdateCommand
	Capabilities []string
	PackfileData []byte
}

// RefUpdateCommand represents a single reference update command in a push.
// Used by SSH protocol handler.
type RefUpdateCommand struct {
	RefName string // Reference name (e.g., refs/heads/main)
	OldHash string // Current hash (zero hash for create)
	NewHash string // New hash (zero hash for delete)
}

// ReceivePackHandler handles Git Smart HTTP protocol for push operations (executor side)
type ReceivePackHandler struct {
	pathConfig   *repositories.PathConfig
	errorHandler *ErrorHandler
	capabilities *ServerCapabilities
	eventsClient events.RedisStreamClient
	config       *config.Config
}

// NewReceivePackHandler creates a new receive-pack handler for the executor
func NewReceivePackHandler(cfg *config.Config) (*ReceivePackHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	errorHandler := NewErrorHandler(nil)
	capabilities := DefaultServerCapabilities()

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		logger, err := logging.NewLogger(cfg.Logging)
		if err == nil {
			eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
			if err != nil {
				_ = err
			}
		}
	}

	h := &ReceivePackHandler{
		pathConfig:   pathConfig,
		errorHandler: errorHandler,
		capabilities: capabilities,
		eventsClient: eventsClient,
		config:       cfg,
	}
	return h, nil
}

const zeroHash = "0000000000000000000000000000000000000000"

// HandleInfoRefs handles Smart HTTP discovery for receive-pack (push) using native git.
func (h *ReceivePackHandler) HandleInfoRefs(w http.ResponseWriter, r *http.Request) {
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	service := r.URL.Query().Get("service")

	if service != "git-receive-pack" {
		http.Error(w, "Invalid service parameter", http.StatusBadRequest)
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}
	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Smart HTTP service header
	pktW := performance.NewOptimizedPktLineWriter(w)
	_ = pktW.WriteLine("# service=git-receive-pack")
	_ = pktW.Flush()

	// Delegate ref/capability advertisement to native git
	cmd := exec.CommandContext(r.Context(), "git", "receive-pack", "--stateless-rpc", "--advertise-refs", repoPath)
	cmd.Stdout = w
	cmd.Env = gitProtocolEnv(r)

	if err := cmd.Run(); err != nil {
		fmt.Printf("[receive-pack] info/refs error: %v\n", err)
	}
}

// HandleReceivePack handles Git receive-pack POST requests using native git.
func (h *ReceivePackHandler) HandleReceivePack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	userID := r.Header.Get("X-USER-ID")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	if err := repositories.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
		return
	}

	// Serialize writes per repo
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.Lock()
	defer func() {
		repositories.GetSimpleCache().Invalidate(repoPath)
		repoLock.Unlock()
	}()

	bodyBytes, err := decompressRequestBody(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("ERR %s", err.Error()), http.StatusBadRequest)
		return
	}

	// Snapshot refs before push for event detection
	beforeRefs := snapshotRefs(ctx, repoPath)

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	cmd := exec.CommandContext(ctx, "git", "receive-pack", "--stateless-rpc", repoPath)
	cmd.Stdin = bytes.NewReader(bodyBytes)
	cmd.Stdout = w
	cmd.Env = gitProtocolEnv(r)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		fmt.Printf("[receive-pack] error: %v, stderr: %s\n", err, stderr.String())
		return
	}

	// Detect ref changes and emit events
	afterRefs := snapshotRefs(ctx, repoPath)
	h.emitRefChangeEvents(ctx, repoPath, beforeRefs, afterRefs, owner, repo, userID)
}

// snapshotRefs captures current ref state for change detection.
func snapshotRefs(ctx context.Context, repoPath string) map[string]string {
	refs, err := gitcmd.ListRefs(ctx, repoPath)
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(refs))
	for _, ref := range refs {
		m[ref.RefName] = ref.ObjectName
	}
	return m
}

// emitRefChangeEvents compares before/after ref snapshots and emits events for changes.
func (h *ReceivePackHandler) emitRefChangeEvents(ctx context.Context, repoPath string, before, after map[string]string, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	for refName, newOID := range after {
		oldOID, existed := before[refName]
		if existed && oldOID == newOID {
			continue
		}
		if !strings.HasPrefix(refName, "refs/heads/") {
			continue
		}
		oldHashStr := ""
		if existed {
			oldHashStr = oldOID
		}
		h.addCommitEventToStream(ctx, repoPath, oldHashStr, newOID, refName, owner, repoName, userID, events.EventTypePush)
	}
}

func (h *ReceivePackHandler) addCommitEventToStream(ctx context.Context, repoPath string, oldHash string, commitHash string, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if h.eventsClient == nil {
		return
	}

	commit, err := gitcmd.GetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		return
	}

	fullRepoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}

	relativePath := events.GetRelativeRepoPath(fullRepoPath, h.pathConfig.GetRepoRoot())

	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     eventType,
		CommitHash:    commitHash,
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.CommitterDate,
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			_ = err
		}
	}()
}
