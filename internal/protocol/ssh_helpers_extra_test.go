package protocol

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ssh_protocol.go pure helpers ---

// TestCheckSSHPermissions 는 현재 구현이 모든 operation 에 대해 에러를 반환하는
// (미구현 placeholder) 동작을 검증한다.
func TestCheckSSHPermissions(t *testing.T) {
	h := &SSHProtocolHandler{}
	ctx := context.Background()

	tests := []struct {
		name      string
		operation string
		wantMsg   string
	}{
		{"upload-pack not implemented", "git-upload-pack", "not implemented"},
		{"receive-pack not implemented", "git-receive-pack", "not implemented"},
		{"unknown operation", "git-evil", "unknown operation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := h.CheckSSHPermissions(ctx, &SSHRequest{Operation: tt.operation})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// TestValidateSSHGitCommand 는 SSH 로 전달된 git 명령/경로 검증을 다룬다.
func TestValidateSSHGitCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		repoPath string
		wantErr  bool
	}{
		{"valid upload-pack", "git-upload-pack 'a/b.git'", "a/b.git", false},
		{"valid receive-pack", "git-receive-pack 'a/b.git'", "a/b.git", false},
		{"empty command", "", "a/b.git", true},
		{"path traversal dotdot", "git-upload-pack", "../../etc", true},
		{"path traversal dotslash", "git-upload-pack", "a/./b", true},
		{"invalid command", "git-evil-pack 'x'", "a/b.git", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSSHGitCommand(tt.command, tt.repoPath)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestParseSSHGitProtocolHeaders 는 SSH 요청 헤더에서 프로토콜 정보 추출을 다룬다.
func TestParseSSHGitProtocolHeaders(t *testing.T) {
	t.Run("extracts all known headers", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/ssh", nil)
		r.Header.Set("Git-Protocol", "version=2")
		r.Header.Set("SSH-Connection", "1.2.3.4 22")
		r.Header.Set("Git-Capabilities", "multi_ack thin-pack")

		got := ParseSSHGitProtocolHeaders(r)
		assert.Equal(t, "version=2", got["protocol-version"])
		assert.Equal(t, "1.2.3.4 22", got["ssh-connection"])
		assert.Equal(t, "multi_ack thin-pack", got["capabilities"])
	})

	t.Run("absent headers yield empty map", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/ssh", nil)
		got := ParseSSHGitProtocolHeaders(r)
		assert.Empty(t, got)
	})
}

// TestWriteSSHError 는 SSH protocol writer 가 pkt-line ERR 메시지를 쓰는지 검증한다.
func TestWriteSSHError(t *testing.T) {
	var buf bytes.Buffer
	w := NewSSHProtocolWriter(&buf, 4096)
	require.NoError(t, w.WriteSSHError("access denied"))

	out := buf.String()
	assert.Contains(t, out, "ERR access denied")
	assert.Contains(t, out, "0000") // flush packet
}

// TestSSHProtocolWriter_WriteFlush 는 SSH writer 의 버퍼 write/flush 를 검증한다.
func TestSSHProtocolWriter_WriteFlush(t *testing.T) {
	var buf bytes.Buffer
	w := NewSSHProtocolWriter(&buf, 4096)
	n, err := w.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	// 버퍼링되어 있으므로 flush 전엔 비어있음
	assert.Empty(t, buf.String())
	require.NoError(t, w.Flush())
	assert.Equal(t, "hello", buf.String())
}

// TestSSHEndpointMetrics 는 메트릭 카운터들을 검증한다.
func TestSSHEndpointMetrics(t *testing.T) {
	m := NewSSHEndpointMetrics()
	require.NotNil(t, m)

	m.RecordRequest()
	m.RecordRequest()
	m.RecordSuccess()
	m.RecordError()
	m.RecordBytes(1024)
	m.RecordBytes(512)

	assert.Equal(t, int64(2), m.RequestCount)
	assert.Equal(t, int64(1), m.SuccessCount)
	assert.Equal(t, int64(1), m.ErrorCount)
	assert.Equal(t, int64(1536), m.BytesTransferred)
}

// TestReadPktLineFromReader 는 pkt-line 디코딩의 다양한 경로를 다룬다.
func TestReadPktLineFromReader(t *testing.T) {
	t.Run("normal line", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader(pktLine("hello")))
		payload, isFlush, err := readPktLineFromReader(r)
		require.NoError(t, err)
		assert.False(t, isFlush)
		assert.Equal(t, "hello", string(payload))
	})

	t.Run("flush packet", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("0000"))
		payload, isFlush, err := readPktLineFromReader(r)
		require.NoError(t, err)
		assert.True(t, isFlush)
		assert.Nil(t, payload)
	})

	t.Run("empty payload (0004)", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("0004"))
		payload, isFlush, err := readPktLineFromReader(r)
		require.NoError(t, err)
		assert.False(t, isFlush)
		assert.Nil(t, payload)
	})

	t.Run("EOF on length header", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader(""))
		_, _, err := readPktLineFromReader(r)
		require.Error(t, err)
	})

	t.Run("invalid hex length", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("zzzz"))
		_, _, err := readPktLineFromReader(r)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid pkt-line length")
	})

	t.Run("length below 4", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("0002"))
		_, _, err := readPktLineFromReader(r)
		require.Error(t, err)
	})

	t.Run("truncated payload", func(t *testing.T) {
		// 길이는 000a(10) → payload 6 byte 기대인데 짧음
		r := bufio.NewReader(strings.NewReader("000aabc"))
		_, _, err := readPktLineFromReader(r)
		require.Error(t, err)
	})
}

// TestParseSSHReceivePackRequest 는 receive-pack 요청 본문 파싱을 다룬다.
func TestParseSSHReceivePackRequest(t *testing.T) {
	h := &SSHProtocolHandler{}

	t.Run("parses single command with capabilities", func(t *testing.T) {
		old := "0000000000000000000000000000000000000000"
		newH := "1111111111111111111111111111111111111111"
		cmdLine := old + " " + newH + " refs/heads/main\x00report-status side-band-64k"

		var input strings.Builder
		input.WriteString(pktLine(cmdLine))
		input.WriteString("0000")

		req, err := h.parseSSHReceivePackRequest(strings.NewReader(input.String()))
		require.NoError(t, err)
		require.Len(t, req.Commands, 1)
		// 구현은 line 을 Fields 로 나누므로 RefName 에 null+caps 가 붙어있다.
		assert.True(t, strings.HasPrefix(req.Commands[0].RefName, "refs/heads/main"))
		assert.Equal(t, old, req.Commands[0].OldHash)
		assert.Equal(t, newH, req.Commands[0].NewHash)
		assert.Contains(t, req.Capabilities, "report-status")
		assert.Contains(t, req.Capabilities, "side-band-64k")
	})

	t.Run("space-separated capabilities", func(t *testing.T) {
		old := "0000000000000000000000000000000000000000"
		newH := "2222222222222222222222222222222222222222"
		cmdLine := old + " " + newH + " refs/heads/dev report-status"

		input := pktLine(cmdLine) + "0000"
		req, err := h.parseSSHReceivePackRequest(strings.NewReader(input))
		require.NoError(t, err)
		require.Len(t, req.Commands, 1)
		assert.Contains(t, req.Capabilities, "report-status")
	})

	t.Run("multiple commands", func(t *testing.T) {
		old := "0000000000000000000000000000000000000000"
		newH := "3333333333333333333333333333333333333333"
		var input strings.Builder
		input.WriteString(pktLine(old + " " + newH + " refs/heads/a"))
		input.WriteString(pktLine(old + " " + newH + " refs/heads/b"))
		input.WriteString("0000")

		req, err := h.parseSSHReceivePackRequest(strings.NewReader(input.String()))
		require.NoError(t, err)
		assert.Len(t, req.Commands, 2)
	})

	t.Run("malformed command line skipped", func(t *testing.T) {
		// 필드가 3개 미만(2개) 이면 명령으로 인식하지 않고 건너뜀
		var input strings.Builder
		input.WriteString(pktLine("only twofields"))
		input.WriteString("0000")
		req, err := h.parseSSHReceivePackRequest(strings.NewReader(input.String()))
		require.NoError(t, err)
		assert.Empty(t, req.Commands)
	})

	t.Run("packfile data captured", func(t *testing.T) {
		old := "0000000000000000000000000000000000000000"
		newH := "4444444444444444444444444444444444444444"
		var input strings.Builder
		input.WriteString(pktLine(old + " " + newH + " refs/heads/main\x00report-status"))
		// PACK 으로 시작하는 packfile 데이터
		input.WriteString(pktLine("PACKdata-here"))

		req, err := h.parseSSHReceivePackRequest(strings.NewReader(input.String()))
		require.NoError(t, err)
		require.Len(t, req.Commands, 1)
		require.NotNil(t, req.PackfileReader)
		data, _ := io.ReadAll(req.PackfileReader)
		assert.Contains(t, string(data), "PACK")
	})
}

// --- receive_pack.go parsePushCommands ---

// TestParsePushCommands 는 v2 push 명령 파싱을 다룬다.
func TestParsePushCommands(t *testing.T) {
	h := &ReceivePackHandler{}

	t.Run("single ref update", func(t *testing.T) {
		cmds := []string{
			"command=push",
			"agent=git/2.40.0",
			"old-oid 0000000000000000000000000000000000000000",
			"new-oid 1111111111111111111111111111111111111111",
			"ref-name refs/heads/main",
		}
		req := h.parsePushCommands(cmds)
		require.Len(t, req.Commands, 1)
		assert.Equal(t, "refs/heads/main", req.Commands[0].RefName)
		assert.Equal(t, "0000000000000000000000000000000000000000", req.Commands[0].OldHash)
		assert.Equal(t, "1111111111111111111111111111111111111111", req.Commands[0].NewHash)
	})

	t.Run("empty and whitespace lines ignored", func(t *testing.T) {
		cmds := []string{"", "   ", "command=push"}
		req := h.parsePushCommands(cmds)
		assert.Empty(t, req.Commands)
	})

	t.Run("incomplete command produces no ref", func(t *testing.T) {
		cmds := []string{
			"command=push",
			"old-oid 0000000000000000000000000000000000000000",
			"ref-name refs/heads/main", // new-oid 누락
		}
		req := h.parsePushCommands(cmds)
		assert.Empty(t, req.Commands)
	})
}

// --- upload_pack.go pkt-line section readers ---

// TestReadPktLineSection 는 한 섹션의 pkt-line 들을 수집하는 동작을 다룬다.
func TestReadPktLineSection(t *testing.T) {
	t.Run("collects lines until flush", func(t *testing.T) {
		var input strings.Builder
		input.WriteString(pktLine("command=ls-refs\n"))
		input.WriteString(pktLine("peel\n"))
		input.WriteString(pktLine("ref-prefix refs/heads/\n"))
		input.WriteString("0000")

		cmds, err := readPktLineSection(strings.NewReader(input.String()))
		require.NoError(t, err)
		require.Len(t, cmds, 3)
		assert.Equal(t, "command=ls-refs", cmds[0])
		assert.Equal(t, "peel", cmds[1])
		assert.Equal(t, "ref-prefix refs/heads/", cmds[2])
	})

	t.Run("stops at delimiter packet 0001", func(t *testing.T) {
		var input strings.Builder
		input.WriteString(pktLine("command=fetch\n"))
		input.WriteString("0001")
		cmds, err := readPktLineSection(strings.NewReader(input.String()))
		require.NoError(t, err)
		assert.Equal(t, []string{"command=fetch"}, cmds)
	})

	t.Run("EOF ends section", func(t *testing.T) {
		input := pktLine("want abc\n")
		cmds, err := readPktLineSection(strings.NewReader(input))
		require.NoError(t, err)
		assert.Equal(t, []string{"want abc"}, cmds)
	})

	t.Run("exported wrapper ReadPktLineSection", func(t *testing.T) {
		input := pktLine("command=ls-refs\n") + "0000"
		cmds, err := ReadPktLineSection(strings.NewReader(input))
		require.NoError(t, err)
		assert.Equal(t, []string{"command=ls-refs"}, cmds)
	})

	t.Run("readSection method delegates", func(t *testing.T) {
		h := &UploadPackHandler{}
		input := pktLine("peel\n") + "0000"
		cmds, err := h.readSection(strings.NewReader(input))
		require.NoError(t, err)
		assert.Equal(t, []string{"peel"}, cmds)
	})
}

// TestGitProtocolEnv 는 Git-Protocol 헤더가 환경변수로 전파되는지 검증한다.
func TestGitProtocolEnv(t *testing.T) {
	t.Run("header sets GIT_PROTOCOL", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/git-upload-pack", nil)
		r.Header.Set("Git-Protocol", "version=2")
		env := gitProtocolEnv(r)
		assert.Contains(t, env, "GIT_PROTOCOL=version=2")
	})

	t.Run("no header leaves env unchanged", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/git-upload-pack", nil)
		env := gitProtocolEnv(r)
		for _, e := range env {
			assert.NotContains(t, e, "GIT_PROTOCOL=")
		}
	})
}

// --- streamdeadline.go idleReader ---

// TestNewIdleReader 는 idleReader 래퍼의 통과/EOF 동작을 검증한다.
func TestNewIdleReader(t *testing.T) {
	t.Run("nil deadliner returns bare reader", func(t *testing.T) {
		r := strings.NewReader("data")
		assert.Same(t, io.Reader(r), NewIdleReader(r, nil))
	})

	t.Run("wraps and forwards reads", func(t *testing.T) {
		sd := NewStreamDeadliner(&deadlineRecorder{}, time.Second)
		r := NewIdleReader(strings.NewReader("hello world"), sd)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(got))
	})
}

// TestNewIdleWriter_Forwards 는 idleWriter 가 데이터를 그대로 통과시키는지 검증한다.
func TestNewIdleWriter_Forwards(t *testing.T) {
	sd := NewStreamDeadliner(&deadlineRecorder{}, time.Second)
	var buf bytes.Buffer
	w := NewIdleWriter(&buf, sd)
	n, err := w.Write([]byte("payload"))
	require.NoError(t, err)
	assert.Equal(t, 7, n)
	assert.Equal(t, "payload", buf.String())
}

// TestStreamDeadliner_SlideNoop 는 deadline 미지원 conn 에서 Slide* 가
// 에러 없이 동작(no-op)하는지 검증한다.
func TestStreamDeadliner_SlideNoop(t *testing.T) {
	sd := NewStreamDeadliner(&deadlineRecorder{}, time.Second)
	require.NoError(t, sd.Prime())
	require.NoError(t, sd.SlideRead())
	require.NoError(t, sd.SlideWrite())

	t.Run("zero idle also no-op", func(t *testing.T) {
		sd0 := NewStreamDeadliner(&deadlineRecorder{}, 0)
		require.NoError(t, sd0.Prime())
	})
}
