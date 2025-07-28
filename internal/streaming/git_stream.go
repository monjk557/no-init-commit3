// Package streaming provides efficient streaming mechanisms for Git operations
// to handle large repository data transfers without loading everything into memory.
// Optimized for gateway forwarding patterns.
package streaming

import (
	"context"
	"fmt"
	"io"
	"sync"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitStreamer provides streaming functionality for Git operations
type GitStreamer struct {
	repo *gogit.Repository
}

// NewGitStreamer creates a new GitStreamer for the given repository
func NewGitStreamer(repo *gogit.Repository) *GitStreamer {
	return &GitStreamer{
		repo: repo,
	}
}

// PackfileStreamRequest represents a request for streaming a packfile
type PackfileStreamRequest struct {
	Wants        []string // Object hashes that the client wants
	Haves        []string // Object hashes that the client already has
	Capabilities []string // Client capabilities
}

// StreamPackfile streams a packfile to the provided writer using efficient streaming
// without loading all objects into memory at once
func (gs *GitStreamer) StreamPackfile(ctx context.Context, req *PackfileStreamRequest, w io.Writer) error {
	// Validate inputs
	if gs.repo == nil {
		return fmt.Errorf("repository is nil")
	}
	if req == nil {
		return fmt.Errorf("request is nil")
	}
	if w == nil {
		return fmt.Errorf("writer is nil")
	}
	if len(req.Wants) == 0 {
		return fmt.Errorf("no objects requested")
	}

	// Create a pipe for streaming data
	pipeReader, pipeWriter := io.Pipe()

	// Channel for error communication
	errChan := make(chan error, 1)

	// Start packfile generation in a goroutine
	go func() {
		// defer pipeWriter.Close()
		// defer _ = pipeReader.Close()
		defer func() {
			_ = pipeWriter.Close() // 리턴값 무시. 함수에 넣어줘야함. defer 는 바로실행되는 function이어야함.
		}()
		// defer = 함수탈출할때 실행되는것.
		err := gs.generatePackfileStream(ctx, req, pipeWriter)
		errChan <- err
	}()

	// Copy from pipe to destination writer (gateway response)
	_, copyErr := io.Copy(w, pipeReader)

	// Wait for generation to complete and get any errors
	generateErr := <-errChan

	// Return the first error encountered
	if generateErr != nil {
		return fmt.Errorf("packfile generation failed: %w", generateErr)
	}
	if copyErr != nil {
		return fmt.Errorf("packfile streaming failed: %w", copyErr)
	}

	return nil
}

// generatePackfileStream generates packfile data and streams it through the pipe
func (gs *GitStreamer) generatePackfileStream(ctx context.Context, req *PackfileStreamRequest, w io.Writer) error {
	// Create a packet line writer for the stream
	pktWriter := &StreamingPktLineWriter{w: w}

	// Send negotiation response
	if err := pktWriter.writeLine("NAK"); err != nil {
		return fmt.Errorf("failed to write NAK: %w", err)
	}

	// Send packfile start indicator
	if err := pktWriter.writeLine("0008NAK\n"); err != nil {
		return err
	}

	// Flush negotiation phase
	if err := pktWriter.flush(); err != nil {
		return err
	}

	// Stream objects without collecting them all in memory
	return gs.streamObjects(ctx, req, pktWriter)
}

// streamObjects streams Git objects one by one without accumulating in memory
func (gs *GitStreamer) streamObjects(ctx context.Context, req *PackfileStreamRequest, pktWriter *StreamingPktLineWriter) error {
	// Track processed objects to avoid duplicates
	processed := make(map[plumbing.Hash]bool)
	objectCount := 0

	// Send simplified packfile header
	if err := pktWriter.writeLine("# streaming pack\n"); err != nil {
		return err
	}

	// Stream each wanted object and its dependencies
	for _, want := range req.Wants {
		hash := plumbing.NewHash(want)

		// Check if client already has this object
		if gs.clientHasObject(hash.String(), req.Haves) {
			continue
		}

		// Stream this object and its dependencies
		err := gs.streamObjectTree(ctx, hash, processed, req.Haves, pktWriter, &objectCount)
		if err != nil {
			return fmt.Errorf("failed to stream object tree for %s: %w", want, err)
		}

		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Continue processing
		}
	}

	// Send packfile summary
	summary := fmt.Sprintf("# pack completed: %d objects streamed\n", objectCount)
	if err := pktWriter.writeLine(summary); err != nil {
		return err
	}

	// End with flush packet
	return pktWriter.flush()
}

// streamObjectTree streams an object and its dependencies without loading everything into memory
func (gs *GitStreamer) streamObjectTree(ctx context.Context, hash plumbing.Hash, processed map[plumbing.Hash]bool, haves []string, pktWriter *StreamingPktLineWriter, objectCount *int) error {
	// Skip if already processed or client has it
	if processed[hash] || gs.clientHasObject(hash.String(), haves) {
		return nil
	}

	// Mark as processed
	processed[hash] = true

	// Try to get the object
	obj, err := gs.repo.Object(plumbing.AnyObject, hash)
	if err != nil {
		// Object not found - skip silently (might be a partial clone scenario)
		return nil
	}

	// Stream this object
	objectInfo := fmt.Sprintf("object %s %s\n", hash.String(), obj.Type().String())
	if err := pktWriter.writeLine(objectInfo); err != nil {
		return err
	}

	*objectCount++

	// For commits, recursively stream parent commits
	if obj.Type() == plumbing.CommitObject {
		commit, err := object.GetCommit(gs.repo.Storer, hash)
		if err == nil {
			// Stream parent commits (but limit depth to prevent memory issues)
			for _, parentHash := range commit.ParentHashes {
				// Recursive call for parent
				err = gs.streamObjectTree(ctx, parentHash, processed, haves, pktWriter, objectCount)
				if err != nil {
					return err
				}

				// Check for context cancellation
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					// Continue
				}

				// Safety limit to prevent infinite recursion
				if *objectCount > 10000 {
					return fmt.Errorf("object count limit reached (10000) - stopping to prevent memory exhaustion")
				}
			}
		}
	}

	return nil
}

// clientHasObject checks if the client already has the specified object
func (gs *GitStreamer) clientHasObject(objectHash string, haves []string) bool {
	for _, have := range haves {
		if have == objectHash {
			return true
		}
	}
	return false
}

// StreamingPktLineWriter provides packet line writing for streaming operations
type StreamingPktLineWriter struct {
	w  io.Writer
	mu sync.Mutex // Protect concurrent writes
}

// writeLine writes a Git protocol packet line
func (p *StreamingPktLineWriter) writeLine(data string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Calculate packet length (4 bytes for length + data)
	length := len(data) + 4
	line := fmt.Sprintf("%04x%s", length, data)

	_, err := p.w.Write([]byte(line))
	return err
}

// flush writes a flush packet (0000)
func (p *StreamingPktLineWriter) flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, err := p.w.Write([]byte("0000"))
	return err
}

// StreamRefs streams repository references for info/refs endpoint
func (gs *GitStreamer) StreamRefs(ctx context.Context, w io.Writer, capabilities []string) error {
	pktWriter := &StreamingPktLineWriter{w: w}

	// Get all references from the repository
	refs, err := gs.repo.References()
	if err != nil {
		// Empty repository case - send dummy reference
		capabilities := "multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor"
		line := fmt.Sprintf("0000000000000000000000000000000000000000 capabilities^{}\x00%s\n", capabilities)
		return pktWriter.writeLine(line)
	}

	firstRef := true
	refCount := 0

	// Stream each reference
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Format reference line
		var line string
		if firstRef {
			// First reference includes capabilities
			capabilities := "multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor"
			line = fmt.Sprintf("%s %s\x00%s\n", ref.Hash().String(), ref.Name().String(), capabilities)
			firstRef = false
		} else {
			// Subsequent references without capabilities
			line = fmt.Sprintf("%s %s\n", ref.Hash().String(), ref.Name().String())
		}

		refCount++
		return pktWriter.writeLine(line)
	})

	if err != nil {
		return fmt.Errorf("failed to stream references: %w", err)
	}

	// If no refs were streamed (empty repo), send dummy reference
	if refCount == 0 {
		capabilities := "multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag multi_ack_detailed no-done symref=HEAD:refs/heads/main agent=git/collabops-git-executor"
		dummyLine := fmt.Sprintf("0000000000000000000000000000000000000000 capabilities^{}\x00%s\n", capabilities)
		if err := pktWriter.writeLine(dummyLine); err != nil {
			return err
		}
	}

	// End with flush packet
	return pktWriter.flush()
}
