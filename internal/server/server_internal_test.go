package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/connection"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestConfig 는 server 패키지 테스트용 최소 유효 config 를 만든다.
// RepoRoot 는 t.TempDir() 로 실제 존재하는 경로를 주어 SetupRouter 가 등록하는
// 모든 핸들러 생성자(NewPathConfig 등)가 성공하도록 한다.
func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Server: config.ServerConfig{
			Host:            "127.0.0.1",
			Port:            8080,
			BasePath:        "/",
			HealthCheckPath: "/health",
			DocsPath:        "/api-docs",
			EnableDocs:      false,
			TCPGitPort:      5222,
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     30 * time.Second,
		},
		Logging: config.LoggingConfig{
			Level:  "error",
			Format: "json",
			Output: "stderr",
		},
		Git: config.GitConfig{
			RepoRoot: t.TempDir(),
		},
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := newTestConfig(t)
	metrics := connection.NewMetricsCollector("git_executor_test")
	cc := connection.NewConcurrencyController(connection.DefaultConcurrencyConfig(), metrics, nil)
	s, err := New(cfg, cc)
	require.NoError(t, err)
	require.NotNil(t, s)
	return s
}

// TestNew 는 New 가 유효 config 로 정상 Server 를 만드는지 확인한다.
func TestNew(t *testing.T) {
	s := newTestServer(t)
	assert.NotNil(t, s.config)
	assert.NotNil(t, s.logger)
	assert.NotNil(t, s.observabilityManager)
	assert.NotNil(t, s.concurrencyController)
}

// TestNewWithNilConcurrencyController 는 cc 가 nil 이어도 생성에 성공함을 확인한다.
func TestNewWithNilConcurrencyController(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, err := New(newTestConfig(t), nil)
	require.NoError(t, err)
	assert.Nil(t, s.concurrencyController)
}

// TestSetupRouterHealthAndReady 는 SetupRouter 가 반환한 엔진의
// /health, /ready 엔드포인트가 200 + 기대 JSON 을 반환하는지 확인한다.
func TestSetupRouterHealthAndReady(t *testing.T) {
	s := newTestServer(t)
	router := s.SetupRouter()
	require.NotNil(t, router)

	// /health
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var health map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &health))
	assert.Equal(t, "healthy", health["status"])

	// /ready (정상 상태 → ready:true)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var ready map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ready))
	assert.Equal(t, true, ready["ready"])
}

// TestReadyCheckShuttingDown 은 SIGTERM 수신(shuttingDown=true) 후 /ready 가
// 503 + reason:shutting_down 을 반환하는 회귀를 검증한다.
func TestReadyCheckShuttingDown(t *testing.T) {
	s := newTestServer(t)
	s.shuttingDown.Store(true)
	router := s.SetupRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["ready"])
	assert.Equal(t, "shutting_down", body["reason"])
}

// TestSetupRouterAPIInfo 는 BasePath "/" 에서 루트 API info 엔드포인트가
// git-executor 서비스 정보를 반환하는지 확인한다.
func TestSetupRouterAPIInfo(t *testing.T) {
	s := newTestServer(t)
	router := s.SetupRouter()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var info map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "git-executor", info["service"])
	assert.Equal(t, "running", info["status"])
}

// TestSetupRouterWithBasePath 는 BasePath 가 "/" 가 아닐 때 API 그룹이 해당
// prefix 아래로 마운트되는지 확인한다.
func TestSetupRouterWithBasePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newTestConfig(t)
	cfg.Server.BasePath = "/api/v1"
	metrics := connection.NewMetricsCollector("git_executor_test_bp")
	cc := connection.NewConcurrencyController(connection.DefaultConcurrencyConfig(), metrics, nil)
	s, err := New(cfg, cc)
	require.NoError(t, err)

	router := s.SetupRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "git-executor")
}

// TestSetupRouterWithDocsEnabled 는 EnableDocs=true 시 Swagger/OpenAPI 라우트가
// 등록되는지 확인한다. serveOpenAPISpec 은 repo 루트의 spec 파일을 부모 디렉터리
// 탐색으로 찾아 200 + YAML 을 반환한다.
func TestSetupRouterWithDocsEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newTestConfig(t)
	cfg.Server.EnableDocs = true
	metrics := connection.NewMetricsCollector("git_executor_test_docs")
	cc := connection.NewConcurrencyController(connection.DefaultConcurrencyConfig(), metrics, nil)
	s, err := New(cfg, cc)
	require.NoError(t, err)
	router := s.SetupRouter()

	// OpenAPI spec 라우트가 등록되어 spec 파일(repo 루트)을 서빙한다.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api-docs/openapi.yaml", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/x-yaml")

	// 루트 docs 경로는 swagger UI 로 리다이렉트.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api-docs", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusFound, w.Code)
}

// TestServeOpenAPISpec 은 serveOpenAPISpec 가 spec 파일을 부모 디렉터리 탐색으로
// 찾아 YAML 을 200 으로 반환하는 경로를 단독 검증한다 (repo 루트에 spec 존재).
func TestServeOpenAPISpec(t *testing.T) {
	s := newTestServer(t)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)

	s.serveOpenAPISpec(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/x-yaml")
	assert.NotEmpty(t, w.Body.Bytes())
}

// TestGetAddr 는 GetAddr 가 서버 미시작 시 빈 문자열, 시작(server 필드 설정) 후
// 설정된 주소를 반환하는지 확인한다.
func TestGetAddr(t *testing.T) {
	s := newTestServer(t)
	// 시작 전: server 필드 nil → ""
	assert.Equal(t, "", s.GetAddr())

	// server 필드를 직접 설정(Start 의 부수효과를 모사)하여 addr 반환 확인.
	s.server = &http.Server{Addr: "127.0.0.1:9999"}
	assert.Equal(t, "127.0.0.1:9999", s.GetAddr())
}

// TestShutdownNilServer 는 Start 전(server==nil) Shutdown 호출이 panic 없이
// nil 을 반환함을 확인한다.
func TestShutdownNilServer(t *testing.T) {
	s := newTestServer(t)
	require.Nil(t, s.server)
	err := s.Shutdown(context.Background())
	assert.NoError(t, err)
}

// TestShutdownWithServer 는 server 필드가 설정된 경우 Shutdown 이 http.Server
// 를 graceful 종료하는지 확인한다.
func TestShutdownWithServer(t *testing.T) {
	s := newTestServer(t)
	s.server = &http.Server{Addr: "127.0.0.1:0"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 시작하지 않은 http.Server.Shutdown 은 즉시 nil 반환.
	err := s.Shutdown(ctx)
	assert.NoError(t, err)
}

// TestIsDecisionExempt 는 decision-JWT 면제 경로 판정을 테이블 기반으로 검증한다.
func TestIsDecisionExempt(t *testing.T) {
	s := newTestServer(t) // HealthCheckPath=/health, DocsPath=/api-docs

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"root", "/", true},
		{"ready", "/ready", true},
		{"metrics", "/metrics", true},
		{"healthcheck path", "/health", true},
		{"swagger ui", "/api-docs/swagger/index.html", true},
		{"openapi spec", "/api-docs/openapi.yaml", true},
		{"docs base path substring", "/api-docs", true},
		{"git protocol not exempt", "/repos/o/r/git-upload-pack", false},
		{"rest api not exempt", "/repos/o/r/branches", false},
		{"arbitrary not exempt", "/anything-else", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, s.isDecisionExempt(tt.path))
		})
	}
}

// TestIsDecisionExemptEmptyDocsPath 는 DocsPath 가 빈 문자열이면 docs substring
// 매칭이 동작하지 않음을(빈 문자열 strings.Contains 오탐 방지) 확인한다.
func TestIsDecisionExemptEmptyDocsPath(t *testing.T) {
	s := newTestServer(t)
	s.config.Server.DocsPath = ""
	// 빈 DocsPath 일 때 swagger/openapi 외 임의 경로는 면제 아님.
	assert.False(t, s.isDecisionExempt("/repos/o/r"))
	// swagger 경로는 여전히 면제(별도 분기).
	assert.True(t, s.isDecisionExempt("/x/swagger/y"))
}

// TestGinContextMiddleware 는 ginContextMiddleware 가 *gin.Context 를 요청
// 컨텍스트의 protocol.GinContextKey 아래 저장하는지 라우트를 통해 검증한다.
func TestGinContextMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	var stored any
	router.GET("/probe", ginContextMiddleware(), func(c *gin.Context) {
		stored = c.Request.Context().Value(protocol.GinContextKey{})
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, stored)
	_, ok := stored.(*gin.Context)
	assert.True(t, ok, "컨텍스트에 *gin.Context 가 저장돼야 함")
}

// TestGitConcurrencyGateNilController 는 concurrencyController 가 nil 이면 게이트가
// 통과(c.Next)되는지 확인한다.
func TestGitConcurrencyGateNilController(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, err := New(newTestConfig(t), nil) // cc=nil
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/gate",
		s.gitConcurrencyGate(connection.OperationClone),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/gate", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGitConcurrencyGateAllows 는 정상 controller 에서 충분한 슬롯이 있을 때
// 게이트가 요청을 허용함을 확인한다.
func TestGitConcurrencyGateAllows(t *testing.T) {
	s := newTestServer(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/gate",
		s.gitConcurrencyGate(connection.OperationClone),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/gate", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestGitConcurrencyGateOverload 는 슬롯이 0 인 controller 에서 게이트가
// 429 + Retry-After 를 반환하는 과부하 경로를 검증한다.
func TestGitConcurrencyGateOverload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newTestConfig(t)
	metrics := connection.NewMetricsCollector("git_executor_overload")
	// MaxConcurrentClones=0 → 첫 clone 요청도 즉시 거부.
	ccCfg := &connection.ConcurrencyConfig{
		MaxConcurrentClones: 0,
		MaxConcurrentPushes: 0,
		PerTenantLimit:      0,
		PerIPLimit:          0,
		QueueSize:           0,
	}
	cc := connection.NewConcurrencyController(ccCfg, metrics, nil)
	s, err := New(cfg, cc)
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/gate",
		s.gitConcurrencyGate(connection.OperationClone),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/gate", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"))
	assert.Contains(t, w.Body.String(), "OVERLOADED")
}

// TestMintLFSAuthTokenDisabled 는 shared secret 미설정 시 mintLFSAuthToken 이
// 에러를 반환(token 생략 → degrade)하는 경로를 검증한다.
func TestMintLFSAuthTokenDisabled(t *testing.T) {
	cfg := newTestConfig(t)
	tcp, err := NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)

	// DecisionJWT.Secret 이 비어 있으므로 signer 비활성 → 에러.
	token, mintErr := tcp.mintLFSAuthToken("00000000-0000-0000-0000-000000000000", "owner", "repo")
	assert.Error(t, mintErr)
	assert.Empty(t, token)
}

// TestMintLFSAuthTokenSuccess 는 secret + UUID subject 가 주어지면 서명된 토큰을
// 발급하는 성공 경로를 검증한다.
func TestMintLFSAuthTokenSuccess(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Security.DecisionJWT.Secret = "test-shared-secret-please-ignore"
	cfg.Security.DecisionJWT.Issuer = "collabops-o2"
	cfg.Security.DecisionJWT.Audience = "git-executor"
	tcp, err := NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)

	token, mintErr := tcp.mintLFSAuthToken("11111111-1111-1111-1111-111111111111", "owner", "repo")
	require.NoError(t, mintErr)
	assert.NotEmpty(t, token)
}

// TestMintLFSAuthTokenNonUUID 는 secret 이 있어도 subject 가 UUID 가 아니면
// 에러를 반환함(orchestrator 호환)을 검증한다.
func TestMintLFSAuthTokenNonUUID(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Security.DecisionJWT.Secret = "test-shared-secret-please-ignore"
	tcp, err := NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)

	token, mintErr := tcp.mintLFSAuthToken("not-a-uuid", "owner", "repo")
	assert.Error(t, mintErr)
	assert.Empty(t, token)
}

// --- tcp_git_server.go 의 순수 헬퍼 테스트 ---

// TestReadPktLine 은 readPktLine 의 pkt-line 프레임 파싱을 테이블 기반으로 검증한다.
func TestReadPktLine(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		wantData  string
		wantFlush bool
		wantErr   bool
	}{
		{
			name:      "simple payload",
			input:     []byte("0006a\n"), // len=0x0006 → payload 2바이트 "a\n"
			wantData:  "a\n",
			wantFlush: false,
		},
		{
			name:      "flush packet",
			input:     []byte("0000"),
			wantFlush: true,
		},
		{
			name:     "service line",
			input:    []byte("0013git-upload-pack"), // 0x0013=19, payload 15바이트
			wantData: "git-upload-pack",
		},
		{
			name:    "bad hex length",
			input:   []byte("zzzz"),
			wantErr: true,
		},
		{
			name:    "length below minimum",
			input:   []byte("0001"), // n=1 < 4
			wantErr: true,
		},
		{
			name:    "truncated header",
			input:   []byte("00"),
			wantErr: true,
		},
		{
			name:    "truncated payload",
			input:   []byte("0010ab"), // 헤더는 16 길이 주장하지만 payload 부족
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tt.input))
			data, isFlush, err := readPktLine(r)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFlush, isFlush)
			if !tt.wantFlush {
				assert.Equal(t, tt.wantData, string(data))
			}
		})
	}
}

// TestFlushWriter 는 flushWriter 가 매 Write 마다 내부 bufio.Writer 를 즉시
// 하위 writer 로 flush 하는지 확인한다.
func TestFlushWriter(t *testing.T) {
	var underlying bytes.Buffer
	bw := bufio.NewWriter(&underlying)
	fw := &flushWriter{w: bw}

	n, err := fw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	// flush 직후이므로 buffering 없이 하위 buffer 에 도달해 있어야 함.
	assert.Equal(t, "hello", underlying.String())

	n, err = fw.Write([]byte(" world"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, "hello world", underlying.String())
}

// TestNewDuplexTCPServerConstruction 은 NewDuplexTCPServer 가 nil cc 로 정상
// 생성되고 내부 필드를 초기화하는지 확인한다 (events 비활성).
func TestNewDuplexTCPServerConstruction(t *testing.T) {
	cfg := newTestConfig(t)
	tcp, err := NewDuplexTCPServer(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, tcp)
	assert.NotNil(t, tcp.concurrencyController)
	assert.NotNil(t, tcp.connections)
	assert.NotNil(t, tcp.metrics)
	assert.NotNil(t, tcp.timeoutPolicy)
}

// TestNewDuplexTCPServerSharedController 는 외부에서 주입된 cc 를 TCP 서버가
// 그대로 사용(슬롯 공유)하는지 확인한다.
func TestNewDuplexTCPServerSharedController(t *testing.T) {
	cfg := newTestConfig(t)
	metrics := connection.NewMetricsCollector("git_executor_shared")
	cc := connection.NewConcurrencyController(connection.DefaultConcurrencyConfig(), metrics, nil)
	tcp, err := NewDuplexTCPServer(cfg, cc)
	require.NoError(t, err)
	assert.Same(t, cc, tcp.concurrencyController)
}
