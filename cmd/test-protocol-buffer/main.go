package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/performance"
)

// Test data for protocol operations
var testData = []struct {
	name string
	data []byte
}{
	{"small", []byte("hello world")},
	{"medium", bytes.Repeat([]byte("test data "), 1000)},
	{"large", bytes.Repeat([]byte("large test data chunk "), 5000)},
}

func main() {
	fmt.Println("🎯 Protocol Buffer 최적화 성능 테스트")
	fmt.Println("=====================================")

	// Test sideband writing performance
	fmt.Println("\n📊 Sideband Writer 성능 비교:")
	testSidebandWriting()

	// Test packet line writing performance
	fmt.Println("\n📊 Packet Line Writer 성능 비교:")
	testPacketLineWriting()

	fmt.Println("\n✅ Protocol Buffer 최적화 테스트 완료!")
}

func testSidebandWriting() {
	for _, test := range testData {
		fmt.Printf("\n  🔍 %s 데이터 (%d bytes):\n", test.name, len(test.data))

		// Test original (simulated) vs optimized
		var originalBuf, optimizedBuf bytes.Buffer

		// Simulate original string formatting approach
		start := time.Now()
		originalSidebandWrite(&originalBuf, test.data, 0x01)
		originalTime := time.Since(start)

		// Test optimized binary approach
		start = time.Now()
		optimizedSidebandWrite(&optimizedBuf, test.data, 0x01)
		optimizedTime := time.Since(start)

		// Calculate improvement
		improvement := float64(originalTime-optimizedTime) / float64(originalTime) * 100

		fmt.Printf("    ⏱️  Original:  %v\n", originalTime)
		fmt.Printf("    ⚡ Optimized: %v\n", optimizedTime)
		fmt.Printf("    📈 개선율:    %.1f%%\n", improvement)

		// Verify output is identical
		if bytes.Equal(originalBuf.Bytes(), optimizedBuf.Bytes()) {
			fmt.Printf("    ✅ 출력 일치 확인\n")
		} else {
			fmt.Printf("    ❌ 출력 불일치!\n")
			fmt.Printf("    Original:  %s\n", originalBuf.String()[:50])
			fmt.Printf("    Optimized: %s\n", optimizedBuf.String()[:50])
		}
	}
}

func testPacketLineWriting() {
	testStrings := []string{
		"command=ls-refs",
		"want 0123456789abcdef0123456789abcdef01234567",
		"have fedcba9876543210fedcba9876543210fedcba98",
		strings.Repeat("long data ", 100),
	}

	for i, testStr := range testStrings {
		fmt.Printf("\n  🔍 패킷 라인 %d (%d chars):\n", i+1, len(testStr))

		var originalBuf, optimizedBuf bytes.Buffer

		// Test original (simulated) vs optimized
		start := time.Now()
		originalPacketLineWrite(&originalBuf, testStr)
		originalTime := time.Since(start)

		start = time.Now()
		optimizedPacketLineWrite(&optimizedBuf, testStr)
		optimizedTime := time.Since(start)

		improvement := float64(originalTime-optimizedTime) / float64(originalTime) * 100

		fmt.Printf("    ⏱️  Original:  %v\n", originalTime)
		fmt.Printf("    ⚡ Optimized: %v\n", optimizedTime)
		fmt.Printf("    📈 개선율:    %.1f%%\n", improvement)

		// Verify output is identical
		if bytes.Equal(originalBuf.Bytes(), optimizedBuf.Bytes()) {
			fmt.Printf("    ✅ 출력 일치 확인\n")
		} else {
			fmt.Printf("    ❌ 출력 불일치!\n")
		}
	}
}

// originalSidebandWrite simulates the old fmt.Sprintf approach
func originalSidebandWrite(w io.Writer, data []byte, channel byte) {
	chunkSize := 65515
	for len(data) > 0 {
		n := chunkSize - 1
		if n > len(data) {
			n = len(data)
		}
		chunk := data[:n]
		data = data[n:]

		payloadLen := 1 + len(chunk)
		length := payloadLen + 4

		// BOTTLENECK: String formatting
		lengthHex := fmt.Sprintf("%04x", length)
		_, _ = w.Write([]byte(lengthHex))
		_, _ = w.Write([]byte{channel})
		_, _ = w.Write(chunk)
	}
}

// optimizedSidebandWrite uses the optimized binary approach
func optimizedSidebandWrite(w io.Writer, data []byte, channel byte) {
	writer := performance.NewOptimizedSidebandWriter(w, channel)
	_, _ = writer.Write(data)
}

// originalPacketLineWrite simulates the old fmt.Sprintf approach
func originalPacketLineWrite(w io.Writer, data string) {
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	length := len(data) + 4

	// BOTTLENECK: String formatting
	line := fmt.Sprintf("%04x%s", length, data)
	_, _ = w.Write([]byte(line))
}

// optimizedPacketLineWrite uses the optimized binary approach
func optimizedPacketLineWrite(w io.Writer, data string) {
	writer := performance.NewOptimizedPktLineWriter(w)
	_ = writer.WriteLine(data)
}

func init() {
	log.SetOutput(os.Stderr)
}
