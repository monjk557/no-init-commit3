package protocol

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CapabilitiesMockLogger implements the Logger interface for testing capabilities
type CapabilitiesMockLogger struct {
	ErrorLogs    []CapLogEntry
	ProtocolLogs []CapLogEntry
}

type CapLogEntry struct {
	Operation string
	Event     string
	Message   string
	Data      map[string]interface{}
}

func (m *CapabilitiesMockLogger) LogError(operation, errorType, message string, details map[string]interface{}) {
	m.ErrorLogs = append(m.ErrorLogs, CapLogEntry{
		Operation: operation,
		Event:     errorType,
		Message:   message,
		Data:      details,
	})
}

func (m *CapabilitiesMockLogger) LogProtocol(event string, data map[string]interface{}) {
	m.ProtocolLogs = append(m.ProtocolLogs, CapLogEntry{
		Event: event,
		Data:  data,
	})
}

// TestDefaultServerCapabilities tests the default server capabilities
func TestDefaultServerCapabilities(t *testing.T) {
	caps := DefaultServerCapabilities()

	// Test that default capabilities are returned
	assert.NotNil(t, caps)

	// Test common Git capabilities are enabled
	assert.True(t, caps.MultiAck)
	assert.True(t, caps.MultiAckDetailed)
	assert.True(t, caps.ThinPack)
	assert.True(t, caps.SideBand64k)
	assert.True(t, caps.OfsDeltas)
	assert.True(t, caps.Shallow)

	// Test PRD-specific capabilities for Smart HTTP protocol
	assert.True(t, caps.ReportStatus)
	assert.True(t, caps.DeleteRefs)
	assert.True(t, caps.Atomic)

	// Test server information
	assert.Equal(t, "collabops-git-executor/1.0", caps.Agent)
	assert.Equal(t, "HEAD:refs/heads/main", caps.SymRef)
}

// TestServerCapabilities_String tests the string representation
func TestServerCapabilities_String(t *testing.T) {
	caps := DefaultServerCapabilities()
	str := caps.String()

	// Should be non-empty
	assert.NotEmpty(t, str)

	// Should contain capability names (sorted alphabetically)
	assert.Contains(t, str, "agent=collabops-git-executor/1.0")
	assert.Contains(t, str, "atomic")
	assert.Contains(t, str, "multi_ack")
	assert.Contains(t, str, "ofs-delta")
	assert.Contains(t, str, "symref=HEAD:refs/heads/main")

	// Test with minimal capabilities
	minimalCaps := &ServerCapabilities{
		MultiAck: true,
		Agent:    "test-agent",
	}
	minimalStr := minimalCaps.String()
	assert.Contains(t, minimalStr, "agent=test-agent")
	assert.Contains(t, minimalStr, "multi_ack")
	assert.NotContains(t, minimalStr, "thin-pack")

	// Test empty capabilities
	emptyCaps := &ServerCapabilities{}
	emptyStr := emptyCaps.String()
	assert.Equal(t, "", emptyStr)
}

// TestNewClientCapabilities tests client capability creation
func TestNewClientCapabilities(t *testing.T) {
	caps := NewClientCapabilities()

	assert.NotNil(t, caps)
	assert.NotNil(t, caps.Capabilities)
	assert.NotNil(t, caps.WantedRefs)
	assert.NotNil(t, caps.HasRefs)
	assert.False(t, caps.Done)
	assert.Empty(t, caps.Capabilities)
	assert.Empty(t, caps.WantedRefs)
	assert.Empty(t, caps.HasRefs)
}

// TestClientCapabilities_HasCapability tests capability checking
func TestClientCapabilities_HasCapability(t *testing.T) {
	caps := NewClientCapabilities()
	caps.Capabilities["multi_ack"] = ""
	caps.Capabilities["agent"] = "git/2.35.1"
	caps.Capabilities["thin-pack"] = ""

	// Test existing capabilities
	assert.True(t, caps.HasCapability("multi_ack"))
	assert.True(t, caps.HasCapability("agent"))
	assert.True(t, caps.HasCapability("thin-pack"))

	// Test non-existing capabilities
	assert.False(t, caps.HasCapability("non-existent"))
	assert.False(t, caps.HasCapability("side-band"))

	// Test case sensitivity
	assert.False(t, caps.HasCapability("MULTI_ACK"))
	assert.False(t, caps.HasCapability("Agent"))
}

// TestClientCapabilities_GetCapability tests capability value retrieval
func TestClientCapabilities_GetCapability(t *testing.T) {
	caps := NewClientCapabilities()
	caps.Capabilities["multi_ack"] = ""
	caps.Capabilities["agent"] = "git/2.35.1"
	caps.Capabilities["symref"] = "HEAD:refs/heads/main"

	// Test capability with value
	value := caps.GetCapability("agent")
	assert.Equal(t, "git/2.35.1", value)

	// Test capability without value
	value = caps.GetCapability("multi_ack")
	assert.Equal(t, "", value)

	// Test non-existing capability
	value = caps.GetCapability("non-existent")
	assert.Equal(t, "", value)

	// Test symref capability (important for HEAD reference)
	value = caps.GetCapability("symref")
	assert.Equal(t, "HEAD:refs/heads/main", value)
}

// TestNewCapabilityNegotiator tests capability negotiator creation
func TestNewCapabilityNegotiator(t *testing.T) {
	serverCaps := DefaultServerCapabilities()
	logger := &CapabilitiesMockLogger{}

	negotiator := NewCapabilityNegotiator(serverCaps, logger)

	assert.NotNil(t, negotiator)
	assert.Equal(t, serverCaps, negotiator.serverCaps)
	assert.Equal(t, logger, negotiator.logger)

	// Test with nil server capabilities
	negotiator2 := NewCapabilityNegotiator(nil, logger)
	assert.NotNil(t, negotiator2.serverCaps)
	assert.True(t, negotiator2.serverCaps.MultiAck) // Should use defaults
}

// TestCapabilityNegotiator_UpdateSymRef tests symref updating
func TestCapabilityNegotiator_UpdateSymRef(t *testing.T) {
	serverCaps := DefaultServerCapabilities()
	logger := &CapabilitiesMockLogger{}
	negotiator := NewCapabilityNegotiator(serverCaps, logger)

	// Test updating symref
	negotiator.UpdateSymRef("refs/heads/main")
	assert.Equal(t, "HEAD:refs/heads/main", negotiator.serverCaps.SymRef)

	// Test updating to different branch
	negotiator.UpdateSymRef("refs/heads/development")
	assert.Equal(t, "HEAD:refs/heads/development", negotiator.serverCaps.SymRef)

	// Test updating to empty
	negotiator.UpdateSymRef("")
	assert.Equal(t, "HEAD:", negotiator.serverCaps.SymRef)
}

// TestCapabilityNegotiator_WriteRefsWithCapabilities tests reference writing
func TestCapabilityNegotiator_WriteRefsWithCapabilities(t *testing.T) {
	testCases := []struct {
		name            string
		refs            []Reference
		expectedContent []string
		symrefTarget    string
	}{
		{
			name: "NoReferences",
			refs: []Reference{},
			expectedContent: []string{
				"0000000000000000000000000000000000000000 capabilities^{}",
				"agent=collabops-git-executor/1.0",
				"atomic",
				"0000", // Flush packet
			},
		},
		{
			name: "BasicReferences",
			refs: []Reference{
				{Hash: "1234567890123456789012345678901234567890", Name: "refs/heads/main"},
				{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", Name: "refs/heads/dev"},
				{Hash: "fedcbafedcbafedcbafedcbafedcbafedcbafedcba", Name: "refs/tags/v1.0.0"},
			},
			expectedContent: []string{
				"1234567890123456789012345678901234567890 refs/heads/main",
				"abcdefabcdefabcdefabcdefabcdefabcdefabcd refs/heads/dev",
				"fedcbafedcbafedcbafedcbafedcbafedcbafedcba refs/tags/v1.0.0",
				"agent=collabops-git-executor/1.0",
				"0000", // Flush packet
			},
		},
		{
			name: "ReferencesWithSymref",
			refs: []Reference{
				{Hash: "1234567890123456789012345678901234567890", Name: "refs/heads/main"},
				{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", Name: "refs/heads/dev"},
			},
			symrefTarget: "refs/heads/feature",
			expectedContent: []string{
				"1234567890123456789012345678901234567890 refs/heads/main",
				"abcdefabcdefabcdefabcdefabcdefabcdefabcd refs/heads/dev",
				"symref=HEAD:refs/heads/feature",
				"0000", // Flush packet
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			serverCaps := DefaultServerCapabilities()
			logger := &CapabilitiesMockLogger{}
			negotiator := NewCapabilityNegotiator(serverCaps, logger)

			if tc.symrefTarget != "" {
				negotiator.UpdateSymRef(tc.symrefTarget)
			}

			var buf bytes.Buffer
			err := negotiator.WriteRefsWithCapabilities(&buf, tc.refs)
			require.NoError(t, err)

			output := buf.String()
			for _, expected := range tc.expectedContent {
				assert.Contains(t, output, expected, "Expected content not found: %s", expected)
			}

			// Verify logging occurred
			assert.Len(t, logger.ProtocolLogs, 1)
			assert.Equal(t, "capability_advertisement", logger.ProtocolLogs[0].Event)
			assert.Equal(t, len(tc.refs), logger.ProtocolLogs[0].Data["refs_count"])
		})
	}
}

// TestCapabilityNegotiator_ParseClientCapabilities tests client capability parsing
func TestCapabilityNegotiator_ParseClientCapabilities(t *testing.T) {
	testCases := []struct {
		name               string
		input              string
		expectedWantedRefs []string
		expectedHasRefs    []string
		expectedCaps       map[string]string
		expectedDone       bool
		expectedError      bool
	}{
		{
			name:               "EmptyInput",
			input:              "0000",
			expectedWantedRefs: []string{},
			expectedHasRefs:    []string{},
			expectedCaps:       map[string]string{},
			expectedDone:       false,
			expectedError:      false,
		},
		{
			name: "BasicWantRequest",
			input: "0032want 1234567890123456789012345678901234567890\n" +
				"0000",
			expectedWantedRefs: []string{"1234567890123456789012345678901234567890"},
			expectedHasRefs:    []string{},
			expectedCaps:       map[string]string{},
			expectedDone:       false,
			expectedError:      false,
		},
		{
			name: "WantWithCapabilities",
			input: "0046want 1234567890123456789012345678901234567890\x00multi_ack thin-pack\n" +
				"0000",
			expectedWantedRefs: []string{"1234567890123456789012345678901234567890"},
			expectedHasRefs:    []string{},
			expectedCaps:       map[string]string{"multi_ack": "", "thin-pack": ""},
			expectedDone:       false,
			expectedError:      false,
		},
		{
			name: "CompleteNegotiation",
			input: "004dwant 1234567890123456789012345678901234567890\x00multi_ack agent=git/2.35.1\n" +
				"0032have abcdefabcdefabcdefabcdefabcdefabcdefabcd\n" +
				"0009done\n" +
				"0000",
			expectedWantedRefs: []string{"1234567890123456789012345678901234567890"},
			expectedHasRefs:    []string{"abcdefabcdefabcdefabcdefabcdefabcdefabcd"},
			expectedCaps:       map[string]string{"multi_ack": "", "agent": "git/2.35.1"},
			expectedDone:       true,
			expectedError:      false,
		},
		{
			name:          "InvalidPacketLength",
			input:         "zzzz",
			expectedError: true,
		},
		{
			name:          "PacketTooShort",
			input:         "0003",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			serverCaps := DefaultServerCapabilities()
			logger := &CapabilitiesMockLogger{}
			negotiator := NewCapabilityNegotiator(serverCaps, logger)

			reader := strings.NewReader(tc.input)
			caps, err := negotiator.ParseClientCapabilities(reader)

			if tc.expectedError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedWantedRefs, caps.WantedRefs)
			assert.Equal(t, tc.expectedHasRefs, caps.HasRefs)
			assert.Equal(t, tc.expectedCaps, caps.Capabilities)
			assert.Equal(t, tc.expectedDone, caps.Done)

			// Verify logging occurred
			if len(tc.expectedWantedRefs) > 0 || len(tc.expectedHasRefs) > 0 || len(tc.expectedCaps) > 0 {
				assert.Len(t, logger.ProtocolLogs, 1)
				assert.Equal(t, "client_capabilities_parsed", logger.ProtocolLogs[0].Event)
			}
		})
	}
}

// TestCapabilityNegotiator_ValidateClientCapabilities tests client capability validation
func TestCapabilityNegotiator_ValidateClientCapabilities(t *testing.T) {
	serverCaps := DefaultServerCapabilities()
	logger := &CapabilitiesMockLogger{}
	negotiator := NewCapabilityNegotiator(serverCaps, logger)

	testCases := []struct {
		name          string
		clientCaps    map[string]string
		expectedError bool
		errorContains string
	}{
		{
			name:          "ValidCapabilities",
			clientCaps:    map[string]string{"multi_ack": "", "thin-pack": ""},
			expectedError: false,
		},
		{
			name:          "EmptyCapabilities",
			clientCaps:    map[string]string{},
			expectedError: false,
		},
		{
			name:          "UnsupportedThinPack",
			clientCaps:    map[string]string{"thin-pack": ""},
			expectedError: false, // Server supports thin-pack
		},
		{
			name:          "UnsupportedSideBand64k",
			clientCaps:    map[string]string{"side-band-64k": ""},
			expectedError: false, // Server supports side-band-64k
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientCaps := NewClientCapabilities()
			clientCaps.Capabilities = tc.clientCaps

			err := negotiator.ValidateClientCapabilities(clientCaps)

			if tc.expectedError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				// Verify logging occurred
				assert.Len(t, logger.ProtocolLogs, 1)
				assert.Equal(t, "capability_validation", logger.ProtocolLogs[0].Event)
			}

			// Reset logger for next test
			logger.ProtocolLogs = []CapLogEntry{}
		})
	}
}

// TestCapabilityNegotiator_GetNegotiatedCapabilities tests capability negotiation
func TestCapabilityNegotiator_GetNegotiatedCapabilities(t *testing.T) {
	serverCaps := DefaultServerCapabilities()
	logger := &CapabilitiesMockLogger{}
	negotiator := NewCapabilityNegotiator(serverCaps, logger)

	testCases := []struct {
		name         string
		clientCaps   map[string]string
		expectedCaps []string
	}{
		{
			name:         "NoCommonCapabilities",
			clientCaps:   map[string]string{},
			expectedCaps: []string{},
		},
		{
			name:         "AllClientCapabilitiesSupported",
			clientCaps:   map[string]string{"multi_ack": "", "thin-pack": "", "side-band-64k": ""},
			expectedCaps: []string{"multi_ack", "thin-pack", "side-band-64k"},
		},
		{
			name:         "PartialCapabilitiesSupported",
			clientCaps:   map[string]string{"multi_ack": "", "unsupported": "", "thin-pack": ""},
			expectedCaps: []string{"multi_ack", "thin-pack"},
		},
		{
			name:         "CapabilitiesWithValues",
			clientCaps:   map[string]string{"agent": "git/2.35.1", "multi_ack": ""},
			expectedCaps: []string{"multi_ack"}, // agent is not negotiated
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientCaps := NewClientCapabilities()
			clientCaps.Capabilities = tc.clientCaps

			negotiated := negotiator.GetNegotiatedCapabilities(clientCaps)

			// Convert to slice for easier comparison
			var result []string
			for cap := range negotiated {
				result = append(result, cap)
			}

			assert.ElementsMatch(t, tc.expectedCaps, result)
		})
	}
}

// TestCapabilityNegotiation_PRDScenarios tests PRD-specific capability scenarios
func TestCapabilityNegotiation_PRDScenarios(t *testing.T) {
	t.Run("GitUploadPackCapabilities", func(t *testing.T) {
		// Test capabilities for git-upload-pack (clone/fetch operations)
		serverCaps := DefaultServerCapabilities()
		logger := &CapabilitiesMockLogger{}
		negotiator := NewCapabilityNegotiator(serverCaps, logger)

		clientCaps := NewClientCapabilities()
		clientCaps.Capabilities = map[string]string{
			"multi_ack_detailed": "",
			"thin-pack":          "",
			"side-band-64k":      "",
			"ofs-delta":          "",
			"shallow":            "",
			"agent":              "git/2.35.1",
		}

		negotiated := negotiator.GetNegotiatedCapabilities(clientCaps)

		// Should include common fetch capabilities
		expectedCaps := []string{
			"multi_ack_detailed",
			"thin-pack",
			"side-band-64k",
			"ofs-delta",
			"shallow",
		}

		for _, cap := range expectedCaps {
			assert.Contains(t, negotiated, cap, "Expected capability %s for upload-pack", cap)
		}
	})

	t.Run("HTTPGatewayCapabilities", func(t *testing.T) {
		// Test capabilities when accessed via HTTP Gateway (PRD scenario)
		serverCaps := DefaultServerCapabilities()
		logger := &CapabilitiesMockLogger{}
		negotiator := NewCapabilityNegotiator(serverCaps, logger)

		// Simulate typical HTTP Git client capabilities
		clientCaps := NewClientCapabilities()
		clientCaps.Capabilities = map[string]string{
			"multi_ack":     "",
			"thin-pack":     "",
			"side-band-64k": "",
			"ofs-delta":     "",
			"agent":         "git/2.35.1.libgit2",
		}

		// Validate that HTTP gateway can handle these capabilities
		err := negotiator.ValidateClientCapabilities(clientCaps)
		assert.NoError(t, err, "HTTP Gateway should support standard Git capabilities")

		negotiated := negotiator.GetNegotiatedCapabilities(clientCaps)
		assert.NotEmpty(t, negotiated, "Should have negotiated capabilities for HTTP Gateway")
	})

	t.Run("SymrefHandling", func(t *testing.T) {
		// Test symref capability for HEAD reference handling (important for PRD)
		serverCaps := DefaultServerCapabilities()
		logger := &CapabilitiesMockLogger{}
		negotiator := NewCapabilityNegotiator(serverCaps, logger)

		// Update to different branch as per PRD scenarios
		negotiator.UpdateSymRef("refs/heads/development")

		// Test that symref is properly advertised
		refs := []Reference{
			{Hash: "1234567890123456789012345678901234567890", Name: "refs/heads/development"},
		}

		var buf bytes.Buffer
		err := negotiator.WriteRefsWithCapabilities(&buf, refs)
		require.NoError(t, err)

		output := buf.String()
		assert.Contains(t, output, "symref=HEAD:refs/heads/development")
	})
}

// TestCapabilityNegotiation_EdgeCases tests edge cases and error conditions
func TestCapabilityNegotiation_EdgeCases(t *testing.T) {
	t.Run("NilLogger", func(t *testing.T) {
		serverCaps := DefaultServerCapabilities()
		negotiator := NewCapabilityNegotiator(serverCaps, nil)

		// Should not panic with nil logger
		clientCaps := NewClientCapabilities()
		err := negotiator.ValidateClientCapabilities(clientCaps)
		assert.NoError(t, err)

		var buf bytes.Buffer
		err = negotiator.WriteRefsWithCapabilities(&buf, []Reference{})
		assert.NoError(t, err)
	})

	t.Run("ServerCapabilitiesDisabled", func(t *testing.T) {
		// Test with most server capabilities disabled
		serverCaps := &ServerCapabilities{
			MultiAck: true, // Only basic capability
			ThinPack: true, // Enable thin-pack
			Agent:    "minimal-server",
			// SideBand64k defaults to false
		}
		logger := &CapabilitiesMockLogger{}
		negotiator := NewCapabilityNegotiator(serverCaps, logger)

		clientCaps := NewClientCapabilities()
		clientCaps.Capabilities = map[string]string{
			"thin-pack": "", // Server supports this
			"multi_ack": "", // Server supports this
		}

		// Should not error when client requests supported capabilities
		err := negotiator.ValidateClientCapabilities(clientCaps)
		assert.NoError(t, err)

		// Should only negotiate supported capabilities
		negotiated := negotiator.GetNegotiatedCapabilities(clientCaps)
		assert.Contains(t, negotiated, "multi_ack")
		assert.Contains(t, negotiated, "thin-pack")
		assert.NotContains(t, negotiated, "side-band-64k")
	})

	t.Run("MalformedCapabilityStrings", func(t *testing.T) {
		serverCaps := DefaultServerCapabilities()
		logger := &CapabilitiesMockLogger{}
		negotiator := NewCapabilityNegotiator(serverCaps, logger)

		// Test with malformed capability parsing
		clientCaps := NewClientCapabilities()

		// This should not panic
		err := negotiator.parseCapabilityString("=value", clientCaps)
		assert.NoError(t, err)
		assert.Equal(t, "value", clientCaps.Capabilities[""])

		err = negotiator.parseCapabilityString("key=", clientCaps)
		assert.NoError(t, err)
		assert.Equal(t, "", clientCaps.Capabilities["key"])

		err = negotiator.parseCapabilityString("key=value=more", clientCaps)
		assert.NoError(t, err)
		assert.Equal(t, "value=more", clientCaps.Capabilities["key"])
	})
}
