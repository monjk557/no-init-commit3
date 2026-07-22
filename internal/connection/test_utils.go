package connection

import (
	"time"
)

// testTimeoutPolicy creates a standardized timeout policy for testing
func testTimeoutPolicy() *TimeoutPolicy {
	return &TimeoutPolicy{
		HandshakeTimeout:      500 * time.Millisecond,
		AuthTimeout:           1 * time.Second,
		ProtocolNegTimeout:    2 * time.Second,
		OperationIdleTimeout:  5 * time.Second,
		OperationMaxTimeout:   30 * time.Second,
		StallThreshold:        2 * time.Second,
		ProgressCheckInterval: 500 * time.Millisecond,
	}
}
