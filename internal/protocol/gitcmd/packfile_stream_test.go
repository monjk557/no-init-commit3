package gitcmd

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"io"
	"testing"
)

// buildFakePackfile creates a minimal valid packfile (header + trailer, zero objects).
func buildFakePackfile(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer

	// Header: "PACK" + version 2 + 0 objects
	buf.WriteString("PACK")
	binary.Write(&buf, binary.BigEndian, uint32(2)) // version
	binary.Write(&buf, binary.BigEndian, uint32(0)) // 0 objects

	// Trailer: SHA-1 of header
	h := sha1.Sum(buf.Bytes())
	buf.Write(h[:])

	return buf.Bytes()
}

// buildFakePackfileWithPayload creates a packfile with some extra payload bytes
// between header and trailer (simulating object data).
func buildFakePackfileWithPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer

	// Header
	buf.WriteString("PACK")
	binary.Write(&buf, binary.BigEndian, uint32(2))
	binary.Write(&buf, binary.BigEndian, uint32(1)) // 1 "object"

	// Payload (fake object data)
	buf.Write(payload)

	// Trailer: SHA-1 of everything so far
	h := sha1.Sum(buf.Bytes())
	buf.Write(h[:])

	return buf.Bytes()
}

func TestCopyPackfileFromStream_EmptyPackfile(t *testing.T) {
	pack := buildFakePackfile(t)
	// Append extra bytes AFTER the packfile (simulating TCP stream with trailing data)
	extra := []byte("EXTRA_TCP_DATA_AFTER_PACKFILE")
	stream := io.MultiReader(bytes.NewReader(pack), bytes.NewReader(extra))

	var dst bytes.Buffer
	n, err := CopyPackfileFromStream(stream, &dst)
	if err != nil {
		t.Fatalf("CopyPackfileFromStream: %v", err)
	}
	if n != int64(len(pack)) {
		t.Errorf("wrote %d bytes, want %d", n, len(pack))
	}
	if !bytes.Equal(dst.Bytes(), pack) {
		t.Errorf("output mismatch: got %d bytes, want %d bytes", dst.Len(), len(pack))
	}
}

func TestCopyPackfileFromStream_WithPayload(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB, 0xCD}, 500)
	pack := buildFakePackfileWithPayload(t, payload)

	// Simulate TCP: packfile followed by extra data
	extra := bytes.Repeat([]byte{0xFF}, 100)
	stream := io.MultiReader(bytes.NewReader(pack), bytes.NewReader(extra))

	var dst bytes.Buffer
	n, err := CopyPackfileFromStream(stream, &dst)
	if err != nil {
		t.Fatalf("CopyPackfileFromStream: %v", err)
	}
	if n != int64(len(pack)) {
		t.Errorf("wrote %d bytes, want %d", n, len(pack))
	}
	if !bytes.Equal(dst.Bytes(), pack) {
		t.Errorf("output mismatch")
	}
}

func TestCopyPackfileFromStream_LargePayload(t *testing.T) {
	// 1MB payload to test chunked reading
	payload := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 256*1024)
	pack := buildFakePackfileWithPayload(t, payload)

	stream := bytes.NewReader(pack)

	var dst bytes.Buffer
	n, err := CopyPackfileFromStream(stream, &dst)
	if err != nil {
		t.Fatalf("CopyPackfileFromStream: %v", err)
	}
	if n != int64(len(pack)) {
		t.Errorf("wrote %d bytes, want %d", n, len(pack))
	}
	if !bytes.Equal(dst.Bytes(), pack) {
		t.Errorf("output mismatch")
	}
}

func TestCopyPackfileFromStream_EOF(t *testing.T) {
	// If stream closes exactly at packfile end, should still work
	pack := buildFakePackfile(t)
	stream := bytes.NewReader(pack)

	var dst bytes.Buffer
	n, err := CopyPackfileFromStream(stream, &dst)
	if err != nil {
		t.Fatalf("CopyPackfileFromStream: %v", err)
	}
	if n != int64(len(pack)) {
		t.Errorf("wrote %d bytes, want %d", n, len(pack))
	}
}

func TestRingBuf_Basic(t *testing.T) {
	rb := newRingBuf(3)

	// Push 3 bytes → ring should be full
	if _, ok := rb.push('a'); ok {
		t.Error("push 'a': should not evict")
	}
	if _, ok := rb.push('b'); ok {
		t.Error("push 'b': should not evict")
	}
	if _, ok := rb.push('c'); ok {
		t.Error("push 'c': should not evict")
	}
	if !rb.full() {
		t.Error("ring should be full after 3 pushes")
	}

	// Push 'd' → evict 'a'
	evicted, ok := rb.push('d')
	if !ok || evicted != 'a' {
		t.Errorf("push 'd': evicted=%c ok=%v, want 'a' true", evicted, ok)
	}

	// Ring should now contain [b, c, d]
	if !rb.equals([]byte{'b', 'c', 'd'}) {
		t.Error("ring content mismatch")
	}
}

func TestRingBuf_Equals(t *testing.T) {
	rb := newRingBuf(4)
	for _, b := range []byte{1, 2, 3, 4, 5, 6} {
		rb.push(b)
	}
	// After pushing 1,2,3,4,5,6 into size-4 ring: [3,4,5,6]
	if !rb.equals([]byte{3, 4, 5, 6}) {
		t.Error("ring should contain [3,4,5,6]")
	}
	if rb.equals([]byte{4, 5, 6, 3}) {
		t.Error("order matters")
	}
}

func TestPackfileObjectCount(t *testing.T) {
	header := make([]byte, 12)
	copy(header[0:4], "PACK")
	binary.BigEndian.PutUint32(header[4:8], 2)   // version 2
	binary.BigEndian.PutUint32(header[8:12], 42) // 42 objects

	count, err := PackfileObjectCount(header)
	if err != nil {
		t.Fatalf("PackfileObjectCount: %v", err)
	}
	if count != 42 {
		t.Errorf("count = %d, want 42", count)
	}
}

func TestPackfileObjectCount_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		header []byte
	}{
		{"too short", []byte("PAC")},
		{"bad signature", []byte("NOTPACK12345")},
		{"bad version", func() []byte {
			h := make([]byte, 12)
			copy(h, "PACK")
			binary.BigEndian.PutUint32(h[4:8], 99)
			return h
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := PackfileObjectCount(tt.header); err == nil {
				t.Error("expected error")
			}
		})
	}
}
