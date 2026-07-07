package observability

import (
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/protocol/gitcmd"
)

// TestRecordTransfer_Branches drives every conditional branch of
// ApplicationMetrics.RecordTransfer: default transport, error result, and the
// optional TTFB/Bytes/Objects/Reused counters (both present and absent).
func TestRecordTransfer_Branches(t *testing.T) {
	m := NewApplicationMetrics()

	cases := []struct {
		name  string
		stats gitcmd.TransferStats
	}{
		{
			name: "full success http upload-pack with all counters",
			stats: gitcmd.TransferStats{
				Operation: "upload-pack",
				Transport: "http",
				CPUTime:   50 * time.Millisecond,
				WallTime:  120 * time.Millisecond,
				TTFB:      10 * time.Millisecond,
				Bytes:     4096,
				Objects:   12,
				Reused:    3,
				Success:   true,
			},
		},
		{
			name: "error result with empty transport falls back to unknown",
			stats: gitcmd.TransferStats{
				Operation: "receive-pack",
				Transport: "",
				CPUTime:   5 * time.Millisecond,
				WallTime:  8 * time.Millisecond,
				Success:   false,
			},
		},
		{
			name: "success with zero optional counters skips optional records",
			stats: gitcmd.TransferStats{
				Operation: "upload-pack",
				Transport: "ssh",
				CPUTime:   1 * time.Millisecond,
				WallTime:  2 * time.Millisecond,
				TTFB:      0,
				Bytes:     0,
				Objects:   0,
				Reused:    0,
				Success:   true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// RecordTransfer emits to otel meters; the assertion is that it
			// executes every branch without panicking.
			m.RecordTransfer(tc.stats)
		})
	}
}
