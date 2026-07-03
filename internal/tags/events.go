package tags

import (
	"context"
	"time"

	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
)

// events.go emits tag_push / tag_delete Redis Stream events for the web (REST)
// tag API, mirroring the git-push path (ReceivePackHandler.addTagEventToStream /
// publishRefDeleteEvent). Without this, tags created or deleted through the web
// UI never reached downstream consumers, unlike tags pushed via git — an
// inconsistency between the two write paths. See Issue-2829 / #2830.
//
// As on the push path, the tag hash is peeled to its underlying commit SHA so
// the payload only ever exposes commit SHAs (never the tag object SHA), and the
// emit is async + best-effort so event publishing never blocks or fails the API.

// publishTagPush emits a tag_push event for a web-created tag.
func (h *TagsHandler) publishTagPush(ctx context.Context, repoPath, owner, repo, tagName, tagHash, userID string) {
	if h.eventsClient == nil {
		return
	}

	commitHash, err := gitcmd.PeelToCommit(ctx, repoPath, tagHash)
	if err != nil {
		return
	}
	commit, err := gitcmd.GetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		return
	}

	event := h.buildTagEvent(owner, repo, events.EventTypeTagPush, commitHash, "", tagName, userID)
	if event == nil {
		return
	}
	event.CommitMessage = commit.Message
	event.Timestamp = commit.CommitterDate
	h.emitAsync(event)
}

// publishTagDelete emits a tag_delete event for a web-deleted tag.
// oldCommitHash is the tag's underlying commit SHA captured BEFORE deletion.
func (h *TagsHandler) publishTagDelete(owner, repo, tagName, oldCommitHash, userID string) {
	if h.eventsClient == nil || oldCommitHash == "" {
		return
	}
	event := h.buildTagEvent(owner, repo, events.EventTypeTagDelete, oldCommitHash, oldCommitHash, tagName, userID)
	if event == nil {
		return
	}
	h.emitAsync(event)
}

// buildTagEvent assembles a CommitEvent for a tag ref, resolving the repo's
// stream-relative path. Returns nil if the path can't be resolved.
func (h *TagsHandler) buildTagEvent(owner, repo string, eventType events.CommitEventType, commitHash, oldRev, tagName, userID string) *events.CommitEvent {
	fullRepoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil
	}
	relativePath := events.GetRelativeRepoPath(fullRepoPath, h.pathConfig.GetRepoRoot())
	now := time.Now()
	return &events.CommitEvent{
		RepoPath:   relativePath,
		EventType:  eventType,
		CommitHash: commitHash,
		OldRev:     oldRev,
		RefName:    "refs/tags/" + tagName,
		Pusher:     "git",
		UserID:     userID,
		Timestamp:  now,
		PushedAt:   now,
	}
}

// emitAsync publishes the event on a detached, time-bounded context so a slow or
// failing stream never affects the API response.
func (h *TagsHandler) emitAsync(event *events.CommitEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}
