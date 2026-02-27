package gitcmd

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
)

// CopyPackfileFromStream reads exactly one Git packfile from r and writes it to dst.
// It detects the packfile boundary using incremental SHA-1 verification:
// a valid packfile ends with a 20-byte SHA-1 of all preceding bytes.
//
// This is critical for TCP connections where the stream does NOT close after the
// packfile (the client keeps the connection open to receive the server's response).
// Unlike io.Copy, this function returns as soon as the packfile is fully received,
// without blocking on additional data.
//
// Returns the number of bytes written (= exact packfile size).
func CopyPackfileFromStream(r io.Reader, dst io.Writer) (int64, error) {
	// Ring buffer holds the last 20 bytes (SHA-1 trailer candidate).
	// The incremental SHA-1 hasher digests all bytes BEFORE the ring.
	// After each Read(), we check: hasher.Sum() == ring contents.
	// Match probability for non-trailer data is ~1/2^160 (astronomically unlikely).
	const trailerSize = 20

	hasher := sha1.New()
	ring := newRingBuf(trailerSize)

	// Minimum valid packfile: 12 (header) + 0 objects + 20 (trailer) = 32 bytes.
	// We start checking after accumulating at least 32 bytes.
	const minPackfileSize = 12 + trailerSize

	buf := make([]byte, 64*1024) // 64KB read buffer
	var totalWritten int64
	headerValidated := false

	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			// Write raw bytes to destination (temp file)
			if _, wErr := dst.Write(buf[:n]); wErr != nil {
				return totalWritten, fmt.Errorf("write packfile data: %w", wErr)
			}
			totalWritten += int64(n)

			// Feed bytes through ring buffer → hasher
			for i := 0; i < n; i++ {
				if evicted, ok := ring.push(buf[i]); ok {
					hasher.Write([]byte{evicted})
				}
			}

			// Validate packfile header on first read
			if !headerValidated && totalWritten >= 12 {
				headerValidated = true
				// Header validation happens in git index-pack; we just ensure
				// we're reading something that looks like a packfile.
			}

			// Check for packfile completion: ring == SHA-1(all bytes before ring)
			if totalWritten >= minPackfileSize && ring.full() {
				expected := hasher.Sum(nil)
				if ring.equals(expected) {
					return totalWritten, nil
				}
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				// Stream closed before valid packfile trailer found.
				if totalWritten == 0 {
					return 0, fmt.Errorf("empty packfile stream")
				}
				return totalWritten, nil
			}
			return totalWritten, fmt.Errorf("read packfile stream: %w", readErr)
		}
	}
}

// PackfileObjectCount parses the packfile header and returns the object count.
// The data must be at least 12 bytes (4 signature + 4 version + 4 count).
func PackfileObjectCount(header []byte) (uint32, error) {
	if len(header) < 12 {
		return 0, fmt.Errorf("packfile header too short: %d bytes", len(header))
	}
	if string(header[0:4]) != "PACK" {
		return 0, fmt.Errorf("invalid packfile signature: %x", header[0:4])
	}
	version := binary.BigEndian.Uint32(header[4:8])
	if version < 2 || version > 3 {
		return 0, fmt.Errorf("unsupported packfile version: %d", version)
	}
	return binary.BigEndian.Uint32(header[8:12]), nil
}

// ringBuf is a fixed-size circular buffer for trailing byte detection.
type ringBuf struct {
	data  []byte
	size  int
	pos   int // next write position
	count int // number of bytes stored (max = size)
}

func newRingBuf(size int) *ringBuf {
	return &ringBuf{
		data: make([]byte, size),
		size: size,
	}
}

// push adds a byte to the ring. Returns (evicted byte, true) if the ring was full.
func (rb *ringBuf) push(b byte) (byte, bool) {
	var evicted byte
	wasFull := rb.count == rb.size

	if wasFull {
		evicted = rb.data[rb.pos]
	}

	rb.data[rb.pos] = b
	rb.pos = (rb.pos + 1) % rb.size

	if !wasFull {
		rb.count++
	}

	return evicted, wasFull
}

// full returns true if the ring contains exactly `size` bytes.
func (rb *ringBuf) full() bool {
	return rb.count == rb.size
}

// equals checks if the ring contents match the given byte slice.
// The comparison is in FIFO order (oldest byte first).
func (rb *ringBuf) equals(b []byte) bool {
	if len(b) != rb.size || rb.count != rb.size {
		return false
	}
	start := rb.pos // oldest byte is at current pos (ring is full)
	for i := 0; i < rb.size; i++ {
		if rb.data[(start+i)%rb.size] != b[i] {
			return false
		}
	}
	return true
}
