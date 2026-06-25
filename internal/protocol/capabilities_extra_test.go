package protocol

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pktLine 은 테스트용으로 pkt-line 한 줄을 생성한다.
// "%04x" 길이 헤더 + payload 형태로 git smart 프로토콜 입력을 만든다.
func pktLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

// TestServerCapabilities_String 는 ServerCapabilities.String 이
// 활성화된 기능들을 정렬된 형태로 직렬화하는지 검증한다.
func TestServerCapabilities_String(t *testing.T) {
	t.Run("default capabilities contains expected tokens", func(t *testing.T) {
		sc := DefaultServerCapabilities()
		got := sc.String()

		// 기본 기능들이 포함되어야 함
		for _, want := range []string{
			"multi_ack", "multi_ack_detailed", "thin-pack", "side-band-64k",
			"ofs-delta", "shallow", "report-status", "delete-refs", "atomic",
		} {
			assert.Contains(t, got, want)
		}
		// symref / agent 는 key=value 형태
		assert.Contains(t, got, "symref=HEAD:refs/heads/main")
		assert.Contains(t, got, "agent=collabops-git-executor/1.0")
	})

	t.Run("output is sorted", func(t *testing.T) {
		sc := DefaultServerCapabilities()
		got := sc.String()
		tokens := strings.Fields(got)
		sorted := make([]string, len(tokens))
		copy(sorted, tokens)
		// sort.Strings 가 적용되었는지 — 직접 비교
		for i := 1; i < len(tokens); i++ {
			assert.LessOrEqual(t, tokens[i-1], tokens[i], "tokens should be sorted")
		}
	})

	t.Run("all boolean flags rendered", func(t *testing.T) {
		sc := &ServerCapabilities{
			MultiAck:         true,
			MultiAckDetailed: true,
			NoDone:           true,
			ThinPack:         true,
			SideBand:         true,
			SideBand64k:      true,
			OfsDeltas:        true,
			Shallow:          true,
			DeepenSince:      true,
			DeepenNot:        true,
			DeepenRelative:   true,
			NoProgress:       true,
			IncludeTag:       true,
			ReportStatus:     true,
			DeleteRefs:       true,
			Quiet:            true,
			Atomic:           true,
			PushOptions:      true,
		}
		got := sc.String()
		for _, want := range []string{
			"multi_ack", "multi_ack_detailed", "no-done", "thin-pack",
			"side-band", "side-band-64k", "ofs-delta", "shallow",
			"deepen-since", "deepen-not", "deepen-relative", "no-progress",
			"include-tag", "report-status", "delete-refs", "quiet",
			"atomic", "push-options",
		} {
			assert.Contains(t, got, want)
		}
	})

	t.Run("empty capabilities yield empty string", func(t *testing.T) {
		sc := &ServerCapabilities{}
		assert.Equal(t, "", sc.String())
	})
}

// TestClientCapabilities_Basics 는 ClientCapabilities 의 생성/조회 헬퍼들을 검증한다.
func TestClientCapabilities_Basics(t *testing.T) {
	cc := NewClientCapabilities()
	require.NotNil(t, cc.Capabilities)
	require.Empty(t, cc.WantedRefs)
	require.Empty(t, cc.HasRefs)
	require.False(t, cc.Done)

	cc.Capabilities["thin-pack"] = ""
	cc.Capabilities["agent"] = "git/2.39.0"

	assert.True(t, cc.HasCapability("thin-pack"))
	assert.False(t, cc.HasCapability("missing"))
	assert.Equal(t, "git/2.39.0", cc.GetCapability("agent"))
	assert.Equal(t, "", cc.GetCapability("thin-pack"))
	assert.Equal(t, "", cc.GetCapability("missing"))
}

// TestNewCapabilityNegotiator_NilDefaults 는 nil serverCaps 를 주면
// 기본 기능으로 대체되는지 검증한다.
func TestNewCapabilityNegotiator_NilDefaults(t *testing.T) {
	cn := NewCapabilityNegotiator(nil, nil)
	require.NotNil(t, cn)
	assert.NotEmpty(t, cn.serverCaps.String())

	custom := &ServerCapabilities{ThinPack: true}
	cn2 := NewCapabilityNegotiator(custom, nil)
	assert.Same(t, custom, cn2.serverCaps)
}

// TestCapabilityNegotiator_UpdateSymRef 는 HEAD symref 갱신을 검증한다.
func TestCapabilityNegotiator_UpdateSymRef(t *testing.T) {
	cn := NewCapabilityNegotiator(nil, nil)
	cn.UpdateSymRef("refs/heads/develop")
	assert.Contains(t, cn.serverCaps.String(), "symref=HEAD:refs/heads/develop")
}

// TestCapabilityNegotiator_WriteRefsWithCapabilities 는 ref advertisement 출력을 검증한다.
func TestCapabilityNegotiator_WriteRefsWithCapabilities(t *testing.T) {
	t.Run("no refs writes capabilities on null ref", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		var buf bytes.Buffer
		require.NoError(t, cn.WriteRefsWithCapabilities(&buf, nil))

		out := buf.String()
		assert.Contains(t, out, "0000000000000000000000000000000000000000 capabilities^{}")
		// flush packet 으로 끝남
		assert.True(t, strings.HasSuffix(out, "0000"))
	})

	t.Run("multiple refs: first carries caps, rest plain", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		refs := []Reference{
			{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "refs/heads/main"},
			{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "refs/heads/dev"},
		}
		var buf bytes.Buffer
		require.NoError(t, cn.WriteRefsWithCapabilities(&buf, refs))

		out := buf.String()
		assert.Contains(t, out, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa refs/heads/main")
		assert.Contains(t, out, "\x00") // 첫 ref 에 null + caps
		assert.Contains(t, out, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb refs/heads/dev")
		assert.True(t, strings.HasSuffix(out, "0000"))
	})
}

// TestCapabilityNegotiator_ParseClientCapabilities 는 pkt-line 입력으로부터
// want/have/done/capabilities 를 파싱하는지 검증한다.
func TestCapabilityNegotiator_ParseClientCapabilities(t *testing.T) {
	t.Run("parses want with caps, have, done", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)

		var input strings.Builder
		input.WriteString(pktLine("want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x00multi_ack thin-pack agent=git/2.39.0\n"))
		input.WriteString(pktLine("want bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"))
		input.WriteString(pktLine("have cccccccccccccccccccccccccccccccccccccccc\n"))
		input.WriteString(pktLine("done\n"))
		input.WriteString("0000")

		cc, err := cn.ParseClientCapabilities(strings.NewReader(input.String()))
		require.NoError(t, err)
		assert.Len(t, cc.WantedRefs, 2)
		assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", cc.WantedRefs[0])
		assert.Len(t, cc.HasRefs, 1)
		assert.True(t, cc.Done)
		assert.True(t, cc.HasCapability("multi_ack"))
		assert.True(t, cc.HasCapability("thin-pack"))
		assert.Equal(t, "git/2.39.0", cc.GetCapability("agent"))
	})

	t.Run("flush packet ends parsing", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		input := pktLine("want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n") + "0000"
		cc, err := cn.ParseClientCapabilities(strings.NewReader(input))
		require.NoError(t, err)
		assert.Len(t, cc.WantedRefs, 1)
	})

	t.Run("EOF without flush still returns", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		input := pktLine("want aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
		cc, err := cn.ParseClientCapabilities(strings.NewReader(input))
		require.NoError(t, err)
		assert.Len(t, cc.WantedRefs, 1)
	})

	t.Run("invalid length header errors", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		// "zzzz" 는 hex 가 아님
		_, err := cn.ParseClientCapabilities(strings.NewReader("zzzzdata"))
		require.Error(t, err)
	})

	t.Run("length below 4 errors", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		_, err := cn.ParseClientCapabilities(strings.NewReader("0002"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packet too short")
	})

	t.Run("truncated data errors", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil)
		// 길이는 0010(16) 인데 payload 가 모자람
		_, err := cn.ParseClientCapabilities(strings.NewReader("0010abc"))
		require.Error(t, err)
	})
}

// TestCapabilityNegotiator_ParsePacketLine_InvalidWant 는 잘못된 want/have 라인의
// 에러 경로를 검증한다.
func TestCapabilityNegotiator_ParsePacketLine_InvalidWant(t *testing.T) {
	cn := NewCapabilityNegotiator(nil, nil)

	t.Run("want with null but no hash errors", func(t *testing.T) {
		// "want \x00..." → null 앞부분이 "want " 뿐이라 hash 가 없음 → 에러
		input := pktLine("want \x00multi_ack\n")
		_, err := cn.ParseClientCapabilities(strings.NewReader(input))
		require.Error(t, err)
	})

	t.Run("want with trailing space but no hash errors", func(t *testing.T) {
		// prefix "want " 매칭되지만 hash 필드가 없어 Fields 가 1개 → 에러
		input := pktLine("want \n")
		_, err := cn.ParseClientCapabilities(strings.NewReader(input))
		require.Error(t, err)
	})

	t.Run("have with trailing space but no hash errors", func(t *testing.T) {
		input := pktLine("have \n")
		_, err := cn.ParseClientCapabilities(strings.NewReader(input))
		require.Error(t, err)
	})
}

// TestCapabilityNegotiator_ValidateClientCapabilities 는 클라이언트-서버 기능
// 호환성 검증을 다룬다.
func TestCapabilityNegotiator_ValidateClientCapabilities(t *testing.T) {
	t.Run("compatible caps pass", func(t *testing.T) {
		cn := NewCapabilityNegotiator(nil, nil) // default supports thin-pack & side-band-64k
		cc := NewClientCapabilities()
		cc.Capabilities["thin-pack"] = ""
		cc.Capabilities["side-band-64k"] = ""
		require.NoError(t, cn.ValidateClientCapabilities(cc))
	})

	t.Run("thin-pack unsupported errors", func(t *testing.T) {
		cn := NewCapabilityNegotiator(&ServerCapabilities{}, nil)
		cc := NewClientCapabilities()
		cc.Capabilities["thin-pack"] = ""
		err := cn.ValidateClientCapabilities(cc)
		require.Error(t, err)
		var gpe *GitProtocolError
		require.ErrorAs(t, err, &gpe)
		assert.Equal(t, http.StatusBadRequest, gpe.HTTPStatus)
	})

	t.Run("side-band-64k unsupported errors", func(t *testing.T) {
		cn := NewCapabilityNegotiator(&ServerCapabilities{}, nil)
		cc := NewClientCapabilities()
		cc.Capabilities["side-band-64k"] = ""
		err := cn.ValidateClientCapabilities(cc)
		require.Error(t, err)
	})
}

// TestCapabilityNegotiator_GetNegotiatedCapabilities 는 클라이언트와 서버가
// 공통으로 지원하는 기능만 남기는지 검증한다.
func TestCapabilityNegotiator_GetNegotiatedCapabilities(t *testing.T) {
	cn := NewCapabilityNegotiator(nil, nil) // default

	cc := NewClientCapabilities()
	cc.Capabilities["multi_ack"] = ""
	cc.Capabilities["thin-pack"] = ""
	cc.Capabilities["side-band"] = "" // default 에서 SideBand=false → 제외
	cc.Capabilities["ofs-delta"] = ""
	cc.Capabilities["include-tag"] = ""
	cc.Capabilities["unknown-cap"] = "" // 무시됨

	negotiated := cn.GetNegotiatedCapabilities(cc)
	assert.Contains(t, negotiated, "multi_ack")
	assert.Contains(t, negotiated, "thin-pack")
	assert.Contains(t, negotiated, "ofs-delta")
	assert.Contains(t, negotiated, "include-tag")
	assert.NotContains(t, negotiated, "side-band") // 서버 미지원
	assert.NotContains(t, negotiated, "unknown-cap")

	t.Run("all switch branches with full server caps", func(t *testing.T) {
		full := &ServerCapabilities{
			MultiAck: true, MultiAckDetailed: true, ThinPack: true,
			SideBand: true, SideBand64k: true, OfsDeltas: true,
			Shallow: true, NoProgress: true, IncludeTag: true,
		}
		cn2 := NewCapabilityNegotiator(full, nil)
		cc2 := NewClientCapabilities()
		for _, c := range []string{
			"multi_ack", "multi_ack_detailed", "thin-pack", "side-band",
			"side-band-64k", "ofs-delta", "shallow", "no-progress", "include-tag",
		} {
			cc2.Capabilities[c] = ""
		}
		negotiated := cn2.GetNegotiatedCapabilities(cc2)
		assert.Len(t, negotiated, 9)
	})
}
