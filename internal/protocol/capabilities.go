// Package protocol implements Git Smart HTTP capability negotiation
// for proper protocol compliance and feature advertisement.
package protocol

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ServerCapabilities represents the capabilities advertised by the server
type ServerCapabilities struct {
	// Core capabilities
	MultiAck         bool // multi_ack
	MultiAckDetailed bool // multi_ack_detailed
	NoDone           bool // no-done
	ThinPack         bool // thin-pack
	SideBand         bool // side-band
	SideBand64k      bool // side-band-64k
	OfsDeltas        bool // ofs-delta
	Shallow          bool // shallow
	DeepenSince      bool // deepen-since
	DeepenNot        bool // deepen-not
	DeepenRelative   bool // deepen-relative
	NoProgress       bool // no-progress
	IncludeTag       bool // include-tag

	// Push capabilities
	ReportStatus bool // report-status
	DeleteRefs   bool // delete-refs
	Quiet        bool // quiet
	Atomic       bool // atomic
	PushOptions  bool // push-options

	// Server information
	Agent  string // agent=collabops-git-executor/1.0
	SymRef string // symref=HEAD:refs/heads/main
}

// DefaultServerCapabilities returns the default capabilities for the executor
func DefaultServerCapabilities() *ServerCapabilities {
	return &ServerCapabilities{
		MultiAck:         true,
		MultiAckDetailed: true,
		ThinPack:         true,
		SideBand64k:      true,
		OfsDeltas:        true,
		Shallow:          true,
		DeepenSince:      true,
		DeepenNot:        true,
		NoProgress:       true,
		IncludeTag:       true,
		ReportStatus:     true,
		DeleteRefs:       true,
		Atomic:           true,
		Agent:            "collabops-git-executor/1.0",
		SymRef:           "HEAD:refs/heads/main", // Will be dynamic
	}
}

// String returns the capability string for advertisement
func (sc *ServerCapabilities) String() string {
	var caps []string

	if sc.MultiAck {
		caps = append(caps, "multi_ack")
	}
	if sc.MultiAckDetailed {
		caps = append(caps, "multi_ack_detailed")
	}
	if sc.NoDone {
		caps = append(caps, "no-done")
	}
	if sc.ThinPack {
		caps = append(caps, "thin-pack")
	}
	if sc.SideBand {
		caps = append(caps, "side-band")
	}
	if sc.SideBand64k {
		caps = append(caps, "side-band-64k")
	}
	if sc.OfsDeltas {
		caps = append(caps, "ofs-delta")
	}
	if sc.Shallow {
		caps = append(caps, "shallow")
	}
	if sc.DeepenSince {
		caps = append(caps, "deepen-since")
	}
	if sc.DeepenNot {
		caps = append(caps, "deepen-not")
	}
	if sc.DeepenRelative {
		caps = append(caps, "deepen-relative")
	}
	if sc.NoProgress {
		caps = append(caps, "no-progress")
	}
	if sc.IncludeTag {
		caps = append(caps, "include-tag")
	}
	if sc.ReportStatus {
		caps = append(caps, "report-status")
	}
	if sc.DeleteRefs {
		caps = append(caps, "delete-refs")
	}
	if sc.Quiet {
		caps = append(caps, "quiet")
	}
	if sc.Atomic {
		caps = append(caps, "atomic")
	}
	if sc.PushOptions {
		caps = append(caps, "push-options")
	}

	// Add server information
	if sc.SymRef != "" {
		caps = append(caps, fmt.Sprintf("symref=%s", sc.SymRef))
	}
	if sc.Agent != "" {
		caps = append(caps, fmt.Sprintf("agent=%s", sc.Agent))
	}

	// Sort capabilities for consistent output
	sort.Strings(caps)

	return strings.Join(caps, " ")
}

// ClientCapabilities represents capabilities sent by the client
type ClientCapabilities struct {
	Capabilities map[string]string // capability name -> value (empty string if no value)
	WantedRefs   []string          // refs the client wants
	HasRefs      []string          // refs the client already has
	Done         bool              // client is done with negotiation
}

// NewClientCapabilities creates a new client capabilities instance
func NewClientCapabilities() *ClientCapabilities {
	return &ClientCapabilities{
		Capabilities: make(map[string]string),
		WantedRefs:   make([]string, 0),
		HasRefs:      make([]string, 0),
		Done:         false,
	}
}

// HasCapability checks if the client has a specific capability
func (cc *ClientCapabilities) HasCapability(name string) bool {
	_, exists := cc.Capabilities[name]
	return exists
}

// GetCapability gets a capability value (returns empty string if not present)
func (cc *ClientCapabilities) GetCapability(name string) string {
	value, exists := cc.Capabilities[name]
	if !exists {
		return ""
	}
	return value
}

// Reference represents a Git reference with its hash and name
type Reference struct {
	Hash string // Git object hash
	Name string // Reference name (e.g., refs/heads/main)
}

// CapabilityNegotiator handles capability negotiation between client and server
type CapabilityNegotiator struct {
	serverCaps *ServerCapabilities
	logger     Logger
}

// NewCapabilityNegotiator creates a new capability negotiator
func NewCapabilityNegotiator(serverCaps *ServerCapabilities, logger Logger) *CapabilityNegotiator {
	if serverCaps == nil {
		serverCaps = DefaultServerCapabilities()
	}

	return &CapabilityNegotiator{
		serverCaps: serverCaps,
		logger:     logger,
	}
}

// UpdateSymRef updates the symbolic reference (HEAD) for capability advertisement
func (cn *CapabilityNegotiator) UpdateSymRef(headRef string) {
	cn.serverCaps.SymRef = fmt.Sprintf("HEAD:%s", headRef)
}

// WriteRefsWithCapabilities writes references with capability advertisement
func (cn *CapabilityNegotiator) WriteRefsWithCapabilities(w io.Writer, refs []Reference) error {
	pktWriter := newPktLineWriter(w)

	if len(refs) == 0 {
		// No refs available - write capabilities on null ref
		capLine := fmt.Sprintf("0000000000000000000000000000000000000000 capabilities^{}\x00%s\n", cn.serverCaps.String())
		if err := pktWriter.writeLine(capLine); err != nil {
			return fmt.Errorf("failed to write capabilities: %w", err)
		}
	} else {
		// Write first ref with capabilities
		firstRef := refs[0]
		capLine := fmt.Sprintf("%s %s\x00%s\n", firstRef.Hash, firstRef.Name, cn.serverCaps.String())
		if err := pktWriter.writeLine(capLine); err != nil {
			return fmt.Errorf("failed to write first ref with capabilities: %w", err)
		}

		// Write remaining refs without capabilities
		for _, ref := range refs[1:] {
			refLine := fmt.Sprintf("%s %s\n", ref.Hash, ref.Name)
			if err := pktWriter.writeLine(refLine); err != nil {
				return fmt.Errorf("failed to write ref %s: %w", ref.Name, err)
			}
		}
	}

	// End with flush packet
	if err := pktWriter.flush(); err != nil {
		return fmt.Errorf("failed to write flush packet: %w", err)
	}

	// Log capability advertisement
	if cn.logger != nil {
		cn.logger.LogProtocol("capability_advertisement", map[string]interface{}{
			"capabilities": cn.serverCaps.String(),
			"refs_count":   len(refs),
			"symref":       cn.serverCaps.SymRef,
		})
	}

	return nil
}

// ParseClientCapabilities parses client capabilities from an upload-pack request
func (cn *CapabilityNegotiator) ParseClientCapabilities(body io.Reader) (*ClientCapabilities, error) {
	clientCaps := NewClientCapabilities()

	// Read and parse pkt-line format
	for {
		// Read packet length
		lengthBuf := make([]byte, 4)
		n, err := io.ReadFull(body, lengthBuf)
		if err != nil {
			if err == io.EOF {
				break // End of input
			}
			return nil, fmt.Errorf("failed to read packet length: %w", err)
		}
		if n != 4 {
			break
		}

		// Parse length
		lengthStr := string(lengthBuf)
		if lengthStr == "0000" {
			// Flush packet - end of section
			break
		}

		// Convert hex length to integer
		var length int
		if _, err := fmt.Sscanf(lengthStr, "%04x", &length); err != nil {
			return nil, fmt.Errorf("invalid packet length: %s", lengthStr)
		}

		if length < 4 {
			return nil, fmt.Errorf("packet too short: %d", length)
		}

		// Read packet data
		dataLen := length - 4
		dataBuf := make([]byte, dataLen)
		n, err = io.ReadFull(body, dataBuf)
		if err != nil {
			return nil, fmt.Errorf("failed to read packet data: %w", err)
		}
		if n != dataLen {
			return nil, fmt.Errorf("incomplete packet data")
		}

		line := string(dataBuf)
		if err := cn.parsePacketLine(line, clientCaps); err != nil {
			return nil, fmt.Errorf("failed to parse packet line: %w", err)
		}
	}

	// Log parsed capabilities
	if cn.logger != nil {
		cn.logger.LogProtocol("client_capabilities_parsed", map[string]interface{}{
			"capabilities": clientCaps.Capabilities,
			"wanted_refs":  len(clientCaps.WantedRefs),
			"has_refs":     len(clientCaps.HasRefs),
			"done":         clientCaps.Done,
		})
	}

	return clientCaps, nil
}

// parsePacketLine parses a single packet line and updates client capabilities
func (cn *CapabilityNegotiator) parsePacketLine(line string, clientCaps *ClientCapabilities) error {
	line = strings.TrimSuffix(line, "\n")

	if strings.HasPrefix(line, "want ") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return fmt.Errorf("invalid want line: %s", line)
		}

		hash := parts[1]
		clientCaps.WantedRefs = append(clientCaps.WantedRefs, hash)

		// Check for capabilities (after null byte)
		if strings.Contains(line, "\x00") {
			capParts := strings.Split(line, "\x00")
			if len(capParts) > 1 {
				capStr := capParts[1]
				if err := cn.parseCapabilityString(capStr, clientCaps); err != nil {
					return fmt.Errorf("failed to parse capabilities: %w", err)
				}
			}
		}
	} else if strings.HasPrefix(line, "have ") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return fmt.Errorf("invalid have line: %s", line)
		}

		hash := parts[1]
		clientCaps.HasRefs = append(clientCaps.HasRefs, hash)
	} else if line == "done" {
		clientCaps.Done = true
	}

	return nil
}

// parseCapabilityString parses a capability string like "multi_ack thin-pack agent=git/2.39.0"
func (cn *CapabilityNegotiator) parseCapabilityString(capStr string, clientCaps *ClientCapabilities) error {
	caps := strings.Fields(capStr)

	for _, cap := range caps {
		if strings.Contains(cap, "=") {
			// Capability with value
			parts := strings.SplitN(cap, "=", 2)
			clientCaps.Capabilities[parts[0]] = parts[1]
		} else {
			// Boolean capability
			clientCaps.Capabilities[cap] = ""
		}
	}

	return nil
}

// ValidateClientCapabilities validates that client capabilities are compatible with server
func (cn *CapabilityNegotiator) ValidateClientCapabilities(clientCaps *ClientCapabilities) error {
	// Check for required capabilities or incompatible combinations

	// If client wants thin-pack, server must support it
	if clientCaps.HasCapability("thin-pack") && !cn.serverCaps.ThinPack {
		return NewGitProtocolError("unsupported_capability",
			"Client requested thin-pack but server does not support it",
			http.StatusBadRequest, true)
	}

	// If client wants side-band-64k, server must support it
	if clientCaps.HasCapability("side-band-64k") && !cn.serverCaps.SideBand64k {
		return NewGitProtocolError("unsupported_capability",
			"Client requested side-band-64k but server does not support it",
			http.StatusBadRequest, true)
	}

	// Log validation result
	if cn.logger != nil {
		cn.logger.LogProtocol("capability_validation", map[string]interface{}{
			"client_capabilities": clientCaps.Capabilities,
			"validation_result":   "passed",
		})
	}

	return nil
}

// GetNegotiatedCapabilities returns the capabilities that both client and server support
func (cn *CapabilityNegotiator) GetNegotiatedCapabilities(clientCaps *ClientCapabilities) map[string]string {
	negotiated := make(map[string]string)

	// Check each client capability against server capabilities
	for clientCap, clientValue := range clientCaps.Capabilities {
		switch clientCap {
		case "multi_ack":
			if cn.serverCaps.MultiAck {
				negotiated[clientCap] = clientValue
			}
		case "multi_ack_detailed":
			if cn.serverCaps.MultiAckDetailed {
				negotiated[clientCap] = clientValue
			}
		case "thin-pack":
			if cn.serverCaps.ThinPack {
				negotiated[clientCap] = clientValue
			}
		case "side-band":
			if cn.serverCaps.SideBand {
				negotiated[clientCap] = clientValue
			}
		case "side-band-64k":
			if cn.serverCaps.SideBand64k {
				negotiated[clientCap] = clientValue
			}
		case "ofs-delta":
			if cn.serverCaps.OfsDeltas {
				negotiated[clientCap] = clientValue
			}
		case "shallow":
			if cn.serverCaps.Shallow {
				negotiated[clientCap] = clientValue
			}
		case "no-progress":
			if cn.serverCaps.NoProgress {
				negotiated[clientCap] = clientValue
			}
		case "include-tag":
			if cn.serverCaps.IncludeTag {
				negotiated[clientCap] = clientValue
			}
		default:
			// Unknown capability - ignore silently (Git protocol behavior)
		}
	}

	return negotiated
}
