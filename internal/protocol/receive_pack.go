// Package protocol implements Git Smart HTTP protocol handlers for the executor.
// This file handles receive-pack operations (push) as the executor counterpart to upload-pack (fetch).
package protocol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
)

// ReceivePackRequest represents a parsed receive-pack request from the client
type ReceivePackRequest struct {
	Commands     []RefUpdateCommand
	Capabilities []string
	PackfileData []byte
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

	w.WriteHeader(http.StatusOK)

	pktWriter := performance.NewOptimizedPktLineWriter(w)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

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
		receiveReq, err := h.parseV1ReceivePack(bodyBytes)
		if err != nil {
			_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
			_ = pktWriter.Flush()
			return
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

	receiveReq, err := h.parsePushSection(allCmds, r.Body)
	if err != nil {
		_ = pktWriter.WriteLine(fmt.Sprintf("ERR %s", err.Error()))
		return
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

func (h *ReceivePackHandler) parseV1ReceivePack(body []byte) (*ReceivePackRequest, error) {
	req := &ReceivePackRequest{
		Commands:     make([]RefUpdateCommand, 0),
		Capabilities: make([]string, 0),
	}

	rd := bytes.NewReader(body)
	lenBuf := make([]byte, 4)
	first := true

	for {
		if _, err := io.ReadFull(rd, lenBuf); err != nil {
			if err == io.EOF {
				return req, nil
			}
			return nil, err
		}

		n, err := strconv.ParseInt(string(lenBuf), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid pkt-line length: %w", err)
		}
		if n == 0 {
			remaining, _ := io.ReadAll(rd)
			if len(remaining) > 0 {
				req.PackfileData = remaining
			}
			return req, nil
		}
		if n < 4 {
			return nil, fmt.Errorf("invalid pkt-line length: %d", n)
		}

		dataLen := int(n) - 4
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(rd, data); err != nil {
			return nil, err
		}

		line := strings.TrimSuffix(string(data), "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}

		var capsPart string
		if first && strings.Contains(line, "\x00") {
			parts := strings.SplitN(line, "\x00", 2)
			line = parts[0]
			capsPart = parts[1]
		}
		if first && capsPart != "" {
			req.Capabilities = append(req.Capabilities, strings.Fields(capsPart)...)
		}
		first = false

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		req.Commands = append(req.Commands, RefUpdateCommand{
			RefName: fields[2],
			OldHash: fields[0],
			NewHash: fields[1],
		})
	}
}

func (h *ReceivePackHandler) parsePushSection(cmds []string, body io.Reader) (*ReceivePackRequest, error) {
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
				cmd := RefUpdateCommand{
					RefName: currentRefName,
					OldHash: currentOldOid,
					NewHash: currentNewOid,
				}
				req.Commands = append(req.Commands, cmd)
				currentOldOid = ""
				currentNewOid = ""
			}
		default:
		}
	}

	remaining, err := io.ReadAll(body)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("error reading packfile: %w", err)
	}

	if len(remaining) > 0 {
		req.PackfileData = remaining
	}

	return req, nil
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

	if len(req.PackfileData) > 0 {
		// Use native git index-pack to process packfile (handles thin packs automatically)
		reader := bytes.NewReader(req.PackfileData)
		_, err := gitcmd.IndexPackFromReader(ctx, repoPath, reader)
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

	// Emit event for branch updates
	if !isDelete && cmd.OldHash != cmd.NewHash && strings.HasPrefix(cmd.RefName, "refs/heads/") {
		oldHashStr := ""
		if cmd.OldHash != zeroHash {
			oldHashStr = cmd.OldHash
		}
		h.addCommitEventToStream(ctx, repoPath, oldHashStr, cmd.NewHash, cmd.RefName, owner, repoName, userID, events.EventTypePush)
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
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			_ = err
		}
	}()
}
