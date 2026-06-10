// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// This file handles receive-pack operations (push) as the executor counterpart to upload-pack (fetch).
package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
	"go.uber.org/zap"
)

// ReceivePackRequest represents a parsed receive-pack request from the client
type ReceivePackRequest struct {
	Commands       []RefUpdateCommand
	Capabilities   []string
	PackfileReader io.Reader // streams packfile data directly — never buffered in memory
}

// RefUpdateCommand represents a single reference update command in a push
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

// NewReceivePackHandler creates a new receive-pack handler for the executor.
//
// LFS pointer push-time storage 검증은 제거됨 — git-executor 가 LFS storage 를
// 더이상 소유하지 않는다 (Issue-3052, 옵션 B). LFS object 무결성은 api-service
// verify endpoint 가 책임진다.
func NewReceivePackHandler(cfg *config.Config) (*ReceivePackHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	errorHandler := NewErrorHandler(nil)
	capabilities := DefaultServerCapabilities()

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled {
		logger, err := logging.NewLogger(cfg.Logging)
		if err == nil {
			eventsClient, err = events.NewRedisStreamClientFromConfig(cfg.Events, logger.Logger)
			if err != nil {
				logger.Warn("Failed to initialize Redis events client, continuing without events",
					zap.Error(err))
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

// HandleInfoRefs handles Smart HTTP discovery for receive-pack (push).
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

	// Smart HTTP service announcement
	pktWriter := performance.NewOptimizedPktLineWriter(w)
	_ = pktWriter.WriteLine("# service=git-receive-pack")
	_ = pktWriter.Flush()

	// v1 ref advertisement + capabilities using native git
	caps := []string{
		"report-status",
		"delete-refs",
		"thin-pack",
		"atomic",
	}
	_ = WriteV1Advert(w, repoPath, caps...)
}

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

	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Replace the http.Server's fixed Read/Write deadlines with a
	// per-stream idle deadline. Push bodies (packfiles) and report
	// streams can both run for many minutes; an absolute deadline
	// truncates them mid-flight and surfaces to git as
	// `early EOF` / `invalid index-pack output`. The idle deadline
	// reaps only truly stalled connections, so concurrency slots are
	// never held by a frozen client.
	sd := NewStreamDeadliner(w, h.config.Server.GitProtocolIdleTimeout)
	if err := sd.Prime(); err != nil {
		fmt.Printf("[receive-pack] failed to install stream deadline: %v\n", err)
	}
	r.Body = NewIdleReadCloser(r.Body, sd)

	w.WriteHeader(http.StatusOK)

	pktWriter := performance.NewOptimizedPktLineWriter(NewIdleWriter(w, sd))

	// Stream the request body instead of buffering it entirely in memory.
	// readSection reads only the small pkt-line commands; the remaining
	// r.Body stream (packfile data) is passed directly to git index-pack.
	cmds, err := h.readSection(r.Body)
	if err != nil {
		_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}

	if len(cmds) == 0 {
		_ = pktWriter.Flush()
		return
	}

	// v1 fallback: first line is not command=
	if !strings.HasPrefix(cmds[0], "command=") {
		receiveReq := h.parseV1CommandsFromSection(cmds)
		// After readSection consumed the flush, the remaining r.Body IS the packfile.
		if h.hasPackfileCommands(receiveReq.Commands) {
			receiveReq.PackfileReader = r.Body
		}

		if err := h.processReceivePackRequest(ctx, repoPath, receiveReq, w, owner, repo, userID, pktWriter); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.Flush()
			return
		}
		return
	}

	if strings.HasPrefix(cmds[0], "command=ls-refs") {
		lsRefsOpts, err := h.readSection(r.Body)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}
		allCmds := append(cmds, lsRefsOpts...)
		lsRefsReq, err := ParseLsRefsSection(allCmds)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		if err := handleLsRefsNative(ctx, repoPath, lsRefsReq, pktWriter); err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			return
		}

		_ = pktWriter.Flush()
		return
	} else if !strings.HasPrefix(cmds[0], "command=push") {
		_ = pktWriter.WriteLine("ERR unknown command")
		return
	}

	// command=push handling
	pushOpts, err := h.readSection(r.Body)
	if err != nil {
		_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
	allCmds := append(cmds, pushOpts...)

	receiveReq := h.parsePushCommands(allCmds)
	// After reading all command sections, remaining r.Body IS the packfile.
	if h.hasPackfileCommands(receiveReq.Commands) {
		receiveReq.PackfileReader = r.Body
	}

	err = h.processReceivePackRequest(ctx, repoPath, receiveReq, w, owner, repo, userID, pktWriter)
	if err != nil {
		_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
}

func (h *ReceivePackHandler) readSection(body io.Reader) ([]string, error) {
	return readPktLineSection(body)
}

// parseV1CommandsFromSection parses V1 ref update commands from lines already
// read by readSection. This avoids re-reading the body from the beginning and
// allows the remaining body stream to be used directly as the packfile source.
func (h *ReceivePackHandler) parseV1CommandsFromSection(lines []string) *ReceivePackRequest {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0, len(lines)),
		Capabilities: make([]string, 0),
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// First line may contain capabilities after \x00 separator.
		if i == 0 && strings.Contains(line, "\x00") {
			parts := strings.SplitN(line, "\x00", 2)
			line = parts[0]
			if parts[1] != "" {
				req.Capabilities = append(req.Capabilities, strings.Fields(parts[1])...)
			}
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		req.Commands = append(req.Commands, RefUpdateCommand{
			OldHash: fields[0],
			NewHash: fields[1],
			RefName: fields[2],
		})
	}

	return req
}

// hasPackfileCommands returns true if any command creates or updates a ref
// (i.e., needs packfile data). Delete-only operations have no packfile.
func (h *ReceivePackHandler) hasPackfileCommands(cmds []RefUpdateCommand) bool {
	for _, cmd := range cmds {
		if cmd.NewHash != zeroHash {
			return true
		}
	}
	return false
}

// parsePushCommands parses V2 push commands from pkt-line section lines.
// The packfile reader is set separately in HandleReceivePack.
func (h *ReceivePackHandler) parsePushCommands(cmds []string) *ReceivePackRequest {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	var currentOldOid, currentNewOid, currentRefName string

	for _, cmd := range cmds {
		c := strings.TrimSpace(cmd)
		if c == "" {
			continue
		}

		switch {
		case c == "command=push":
		case strings.HasPrefix(c, "agent="):
		case strings.HasPrefix(c, "old-oid "):
			currentOldOid = strings.TrimSpace(c[8:])
		case strings.HasPrefix(c, "new-oid "):
			currentNewOid = strings.TrimSpace(c[8:])
		case strings.HasPrefix(c, "ref-name "):
			currentRefName = strings.TrimSpace(c[9:])
			if currentOldOid != "" && currentNewOid != "" && currentRefName != "" {
				refCmd := RefUpdateCommand{
					RefName: currentRefName,
					OldHash: currentOldOid,
					NewHash: currentNewOid,
				}
				req.Commands = append(req.Commands, refCmd)
				currentOldOid = ""
				currentNewOid = ""
			}
		default:
		}
	}

	return req
}

const zeroHash = "0000000000000000000000000000000000000000"

func (h *ReceivePackHandler) processReceivePackRequest(ctx context.Context, repoPath string, req *ReceivePackRequest, w http.ResponseWriter, owner, repoName, userID string, pktWriter *pktLineWriter) error {
	if pktWriter == nil {
		pktWriter = newPktLineWriter(w)
	}

	if len(req.Commands) == 0 {
		if err := pktWriter.WriteLine("unpack ok"); err != nil {
			return fmt.Errorf("failed to write unpack status: %w", err)
		}
		return pktWriter.Flush()
	}

	if req.PackfileReader != nil {
		// Stream packfile directly to git index-pack — never buffered in memory.
		_, err := gitcmd.IndexPackFromReader(ctx, repoPath, req.PackfileReader)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("unpack %s", err.Error()))
			// V1 report-status requires ng lines for each ref; without them
			// the client reports "remote failed to report status".
			for _, cmd := range req.Commands {
				_ = pktWriter.WriteLine(fmt.Sprintf("ng %s unpack failed", cmd.RefName))
			}
			return pktWriter.Flush()
		}
	}

	if err := pktWriter.WriteLine("unpack ok"); err != nil {
		return fmt.Errorf("failed to write unpack status: %w", err)
	}

	// LFS pointer push-time storage 검증은 제거됨 (Issue-3052). LFS object 무결성은
	// api-service verify endpoint 가 책임진다. push 시점에는 pointer blob 자체만
	// git object 로 저장되고, 실제 byte 부재는 fetch / pull 시점에 드러난다.

	// Process ref updates
	for _, cmd := range req.Commands {
		err := h.processRefUpdate(ctx, repoPath, cmd, owner, repoName, userID)
		if err != nil {
			statusLine := fmt.Sprintf("ng %s %s", cmd.RefName, err.Error())
			if err := pktWriter.WriteLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref error status: %w", err)
			}
		} else {
			statusLine := fmt.Sprintf("ok %s", cmd.RefName)
			if err := pktWriter.WriteLine(statusLine); err != nil {
				return fmt.Errorf("failed to write ref success status: %w", err)
			}
		}
	}

	if err := pktWriter.Flush(); err != nil {
		return err
	}

	return nil
}

func (h *ReceivePackHandler) processRefUpdate(ctx context.Context, repoPath string, cmd RefUpdateCommand, owner, repoName, userID string) error {
	if !isValidRefName(cmd.RefName) {
		return fmt.Errorf("invalid reference name: %s", cmd.RefName)
	}

	isCreate := cmd.OldHash == zeroHash
	isDelete := cmd.NewHash == zeroHash

	// Verify current ref state (CAS check)
	if !isCreate {
		currentOID, err := gitcmd.VerifyRef(ctx, repoPath, cmd.RefName)
		if err != nil {
			return fmt.Errorf("reference %s does not exist", cmd.RefName)
		}
		if currentOID != cmd.OldHash {
			return fmt.Errorf("reference %s has been modified (expected %s, got %s)", cmd.RefName, cmd.OldHash, currentOID)
		}
	}

	// Verify new object exists
	if !isDelete {
		if _, err := gitcmd.ObjectExists(ctx, repoPath, cmd.NewHash); err != nil {
			return fmt.Errorf("object %s not found in repository", cmd.NewHash)
		}
	}

	// Apply ref update using native git update-ref
	updates := []gitcmd.RefUpdateCmd{{
		RefName: cmd.RefName,
		OldOID:  cmd.OldHash,
		NewOID:  cmd.NewHash,
	}}
	if err := gitcmd.AtomicRefUpdate(ctx, repoPath, updates); err != nil {
		return fmt.Errorf("failed to update reference %s: %w", cmd.RefName, err)
	}

	// Emit event for branch/tag updates
	if !isDelete && cmd.OldHash != cmd.NewHash {
		oldHashStr := ""
		if cmd.OldHash != zeroHash {
			oldHashStr = cmd.OldHash
		}
		switch {
		case strings.HasPrefix(cmd.RefName, "refs/heads/"):
			h.addCommitEventToStream(ctx, repoPath, oldHashStr, cmd.NewHash, cmd.RefName, owner, repoName, userID, events.EventTypePush)
		case strings.HasPrefix(cmd.RefName, "refs/tags/"):
			h.addTagEventToStream(ctx, repoPath, oldHashStr, cmd.NewHash, cmd.RefName, owner, repoName, userID)
		}
	}

	// Emit event for branch/tag deletion
	if isDelete {
		switch {
		case strings.HasPrefix(cmd.RefName, "refs/heads/"):
			h.publishRefDeleteEvent(ctx, repoPath, cmd.OldHash, cmd.RefName, owner, repoName, userID, events.EventTypeBranchDelete)
		case strings.HasPrefix(cmd.RefName, "refs/tags/"):
			h.publishRefDeleteEvent(ctx, repoPath, peelTagDeleteHash(ctx, repoPath, cmd.OldHash), cmd.RefName, owner, repoName, userID, events.EventTypeTagDelete)
		}
	}

	return nil
}

func isValidRefName(refName string) bool {
	if refName == "" {
		return false
	}

	if !strings.HasPrefix(refName, "refs/") {
		return false
	}

	for _, r := range refName {
		if r >= 0 && r <= 31 {
			return false
		}
	}

	invalidChars := []string{" ", "~", "^", ":", "?", "*", "[", "\\", "..", "//", "@{"}
	for _, char := range invalidChars {
		if strings.Contains(refName, char) {
			return false
		}
	}

	if strings.HasSuffix(refName, "/") || strings.HasSuffix(refName, ".lock") {
		return false
	}

	return true
}

func (h *ReceivePackHandler) addCommitEventToStream(ctx context.Context, repoPath string, oldHash string, commitHash string, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if h.eventsClient == nil {
		return
	}

	// Get commit info using native git
	commit, err := gitcmd.GetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		return
	}

	// Get relative path for event
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
		PushedAt:      time.Now(),
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			_ = err
		}
	}()
}

// addTagEventToStream publishes a tag_push event to Redis Stream. The pushed ref
// may point at a lightweight tag (commit), an annotated tag object, or a force
// re-pointed floating major tag. The new (and best-effort old) hash is peeled to
// its underlying commit SHA so the event payload only ever exposes commit SHAs —
// the tag object SHA is never leaked downstream (Issue-2829 decision #4).
func (h *ReceivePackHandler) addTagEventToStream(ctx context.Context, repoPath string, oldHash string, tagHash, refName, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	commitHash, err := gitcmd.PeelToCommit(ctx, repoPath, tagHash)
	if err != nil {
		// Cannot resolve the tag to a commit (e.g. dangling object); skip rather
		// than emit a tag object SHA the orchestrator cannot interpret.
		return
	}

	oldRev := oldHash
	if oldHash != "" {
		if peeled, peelErr := gitcmd.PeelToCommit(ctx, repoPath, oldHash); peelErr == nil {
			oldRev = peeled
		}
	}

	h.addCommitEventToStream(ctx, repoPath, oldRev, commitHash, refName, owner, repoName, userID, events.EventTypeTagPush)
}

// peelTagDeleteHash best-effort peels a deleted tag's old hash to its underlying
// commit SHA. The ref is already gone by the time this runs, but the object
// usually still exists (pre-GC), so peeling keeps tag_delete payloads consistent
// with tag_push (commit SHA only). Falls back to the raw old hash on failure.
func peelTagDeleteHash(ctx context.Context, repoPath, oldHash string) string {
	if oldHash == "" || oldHash == zeroHash {
		return oldHash
	}
	if peeled, err := gitcmd.PeelToCommit(ctx, repoPath, oldHash); err == nil {
		return peeled
	}
	return oldHash
}

// publishRefDeleteEvent publishes a ref deletion event (branch_delete or
// tag_delete) to Redis Stream. Unlike addCommitEventToStream, this does not call
// GetCommitInfo because the ref has already been deleted and the commit hash is
// the old (pre-delete) value.
func (h *ReceivePackHandler) publishRefDeleteEvent(ctx context.Context, repoPath string, oldHash string, refName, owner, repoName, userID string, eventType events.CommitEventType) {
	if h.eventsClient == nil {
		return
	}

	fullRepoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}

	relativePath := events.GetRelativeRepoPath(fullRepoPath, h.pathConfig.GetRepoRoot())

	event := &events.CommitEvent{
		RepoPath:   relativePath,
		EventType:  eventType,
		CommitHash: oldHash,
		OldRev:     oldHash,
		RefName:    refName,
		Pusher:     "git",
		UserID:     userID,
		Timestamp:  time.Now(),
		PushedAt:   time.Now(),
	}

	go func() {
		evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.eventsClient.AddCommitEventToStream(evCtx, event)
	}()
}
