package connection

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// ==============================
// errors.go — 타입드 에러 생성자 / accessor / 분류 헬퍼
// 대부분 0% 였던 순수 함수들이므로 결정적으로 검증한다.
// ==============================

func TestBaseError_Accessors(t *testing.T) {
	cause := errors.New("root cause")
	// NewConnectionError 가 cause 를 품은 BaseError 를 만든다.
	connErr := NewConnectionError("dial", "10.0.0.1:22", cause)

	// Error() 는 cause 가 있을 때 "message: cause" 형식.
	assert.Contains(t, connErr.Error(), "connection dial failed")
	assert.Contains(t, connErr.Error(), "root cause")

	assert.Equal(t, "CONNECTION_dial", connErr.Code())
	assert.Equal(t, ErrorCategoryNetwork, connErr.Category())
	assert.True(t, connErr.IsTemporary())
	assert.True(t, connErr.IsRetryable())
	assert.Equal(t, cause, connErr.Unwrap())
	assert.False(t, connErr.GetTimestamp().IsZero())

	// GetContext 는 생성자가 채운 context 를 반환.
	ctx := connErr.GetContext()
	assert.Equal(t, "10.0.0.1:22", ctx["remote_addr"])

	// SetContext 로 추가 컨텍스트를 넣을 수 있다.
	connErr.SetContext("extra", 42)
	assert.Equal(t, 42, connErr.GetContext()["extra"])
}

func TestBaseError_Error_NoCause(t *testing.T) {
	// cause 가 nil 이면 message 만 반환.
	e := &BaseError{message: "plain message"}
	assert.Equal(t, "plain message", e.Error())

	// context 가 nil 이면 GetContext 는 빈 map 을 반환 (nil 아님).
	assert.NotNil(t, e.GetContext())

	// SetContext 가 nil map 을 초기화한다.
	e.SetContext("k", "v")
	assert.Equal(t, "v", e.GetContext()["k"])
}

func TestErrorConstructors_Smoke(t *testing.T) {
	// 각 생성자가 올바른 category/code 를 가진 에러를 반환하는지 확인.
	to := NewTimeoutError(PhaseAuth, 5*time.Second, "AUTH")
	assert.Equal(t, ErrorCategoryTimeout, to.Category())
	assert.True(t, IsTimeoutError(to))

	cfg := NewConfigurationError("field", 1, "bad field")
	assert.Equal(t, ErrorCategoryConfiguration, cfg.Category())
	assert.True(t, IsConfigurationError(cfg))

	closed := NewConnectionClosedError("read")
	assert.Equal(t, ErrorCategoryLifecycle, closed.Category())
	assert.True(t, IsConnectionError(closed))

	disc := NewClientDisconnectError("read", "1.2.3.4", "operation", errors.New("broken pipe"))
	assert.Equal(t, ErrorCategoryNetwork, disc.Category())
	assert.True(t, IsClientDisconnect(disc))
	assert.True(t, IsConnectionError(disc))

	stalled := NewConnectionStalledError(30*time.Second, 1024)
	assert.Equal(t, ErrorCategoryNetwork, stalled.Category())
	assert.True(t, IsConnectionError(stalled))

	conc := NewConcurrencyError(OperationPush, "RATE_LIMIT", 5, 10)
	assert.Equal(t, ErrorCategoryConcurrency, conc.Category())
	assert.True(t, IsConcurrencyError(conc))

	qf := NewQueueFullError(OperationClone, 100)
	assert.Equal(t, ErrorCategoryConcurrency, qf.Category())
	assert.True(t, IsConcurrencyError(qf))

	val := NewValidationError("name", "", "REQUIRED", "name required")
	assert.Equal(t, ErrorCategoryValidation, val.Category())

	io := NewIOError("write", 512, errors.New("disk full"))
	assert.Equal(t, ErrorCategoryIO, io.Category())

	proto := NewProtocolError("negotiation", "ls-refs", "bad proto", nil)
	assert.Equal(t, ErrorCategoryProtocol, proto.Category())
}

func TestErrorClassificationHelpers(t *testing.T) {
	// retryable / temporary / category 헬퍼는 Error 인터페이스 구현체에만 동작.
	retryable := NewConnectionError("dial", "", nil) // retryable=true, temporary=true
	assert.True(t, IsRetryableError(retryable))
	assert.True(t, IsTemporaryError(retryable))
	assert.Equal(t, ErrorCategoryNetwork, GetErrorCategory(retryable))

	notRetryable := NewConfigurationError("f", 1, "bad") // retryable=false, temporary=false
	assert.False(t, IsRetryableError(notRetryable))
	assert.False(t, IsTemporaryError(notRetryable))

	// 표준 에러는 false / unknown 으로 처리.
	plain := errors.New("plain")
	assert.False(t, IsRetryableError(plain))
	assert.False(t, IsTemporaryError(plain))
	assert.Equal(t, ErrorCategory("unknown"), GetErrorCategory(plain))

	// 분류 헬퍼는 비매칭 타입에 false.
	assert.False(t, IsTimeoutError(plain))
	assert.False(t, IsConfigurationError(plain))
	assert.False(t, IsConnectionError(plain))
	assert.False(t, IsConcurrencyError(plain))
	assert.False(t, IsClientDisconnect(plain))
}

func TestWrapError(t *testing.T) {
	cause := errors.New("underlying")
	wrapped := WrapError(cause, ErrorCategoryIO, "wrapped message")
	assert.Equal(t, ErrorCategoryIO, wrapped.Category())
	assert.Equal(t, "WRAPPED", wrapped.Code())
	assert.Equal(t, cause, wrapped.Unwrap())
	assert.Contains(t, wrapped.Error(), "wrapped message")
}

// errTimeout 은 net.Error 를 구현하여 timeout 분류 경로를 강제한다.
type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

func TestClassifyNetworkError(t *testing.T) {
	// nil 입력은 nil 반환.
	assert.Nil(t, ClassifyNetworkError(nil, "read", "", ""))

	// client disconnect 패턴 매칭.
	disc := ClassifyNetworkError(errors.New("write: broken pipe"), "write", "1.2.3.4", "operation")
	assert.True(t, IsClientDisconnect(disc))

	dropped := ClassifyNetworkError(errors.New("use of closed network connection"), "read", "", "")
	assert.True(t, IsClientDisconnect(dropped))

	// net.Error 타임아웃 → TimeoutError.
	to := ClassifyNetworkError(errTimeout{}, "read", "", "operation")
	assert.True(t, IsTimeoutError(to))

	// 그 외는 ProtocolError 로 폴백.
	proto := ClassifyNetworkError(errors.New("garbage"), "read", "", "neg")
	assert.Equal(t, ErrorCategoryProtocol, proto.Category())
}

func TestErrorAs(t *testing.T) {
	// errorAs 는 net.Error 로 캐스팅 가능할 때만 true.
	var target net.Error
	assert.True(t, errorAs(errTimeout{}, &target))
	assert.False(t, errorAs(errors.New("not net err"), &target))
}

// ==============================
// concurrency_control.go — 메트릭 레코더 / GetClientIP / OperationType
// ==============================

func TestMetricsCollector_RecordMethods(t *testing.T) {
	// 메트릭 레코더는 OTel no-op meter 로도 panic 없이 동작해야 한다.
	mc := NewMetricsCollector("covtest")
	require.NotNil(t, mc)

	assert.NotPanics(t, func() {
		mc.RecordOperationStart(OperationClone, "tenant1")
		mc.RecordOperationComplete(OperationClone, 2*time.Second, true, "tenant1")
		mc.RecordOperationComplete(OperationPush, time.Second, false, "tenant2")
		mc.RecordTimeout("handshake", OperationPush)
		mc.RecordTransferRate(OperationClone, "tenant1", 1024.0)
		mc.RecordStallDetection(OperationPush, "tenant1")
		mc.RecordQueueWait(OperationClone, 100*time.Millisecond)
		mc.RecordConnectionError("reset", OperationClone)
	})
}

func TestOperationType_String(t *testing.T) {
	assert.Equal(t, "clone", OperationClone.String())
	assert.Equal(t, "push", OperationPush.String())
	assert.Equal(t, "unknown", OperationType(99).String())
}

func TestGetClientIP(t *testing.T) {
	// net.Conn 경로 (MockTCPConn 의 RemoteAddr 은 TCPAddr 127.0.0.1).
	conn := &MockTCPConn{}
	assert.Equal(t, "127.0.0.1", GetClientIP(conn))

	// net.Addr 직접 전달 (TCPAddr).
	tcpAddr := &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 9000}
	assert.Equal(t, "10.0.0.5", GetClientIP(tcpAddr))

	// host:port 문자열 파싱이 필요한 비-TCP addr.
	udp := &net.UDPAddr{IP: net.ParseIP("10.0.0.6"), Port: 53}
	assert.Equal(t, "10.0.0.6", GetClientIP(udp))

	// nil net.Conn → 빈 문자열.
	var nilConn net.Conn
	assert.Equal(t, "", GetClientIP(nilConn))

	// nil net.Addr → 빈 문자열.
	var nilAddr net.Addr
	assert.Equal(t, "", GetClientIP(nilAddr))

	// 지원하지 않는 타입 → 빈 문자열.
	assert.Equal(t, "", GetClientIP("not a conn"))
}

func TestConcurrencyController_UnknownOperation(t *testing.T) {
	cc := NewConcurrencyController(&ConcurrencyConfig{
		MaxConcurrentClones: 1,
		MaxConcurrentPushes: 1,
		PerTenantLimit:      1,
		PerIPLimit:          1,
		QueueSize:           1,
	}, NewMetricsCollector("covtest"), zaptest.NewLogger(t))
	defer cc.Stop()

	// 알 수 없는 operation 타입은 ValidationError 를 반환.
	err := cc.RequestOperation(context.Background(), OperationType(42), "t", "ip")
	require.Error(t, err)
	_, ok := err.(*ValidationError)
	assert.True(t, ok, "expected ValidationError, got %T", err)

	// getQueueForOperation 도 알 수 없는 타입에 nil.
	assert.Nil(t, cc.getQueueForOperation(OperationType(42)))
}

func TestConcurrencyController_QueueFull(t *testing.T) {
	// QueueSize 0 + 처리 고루틴이 즉시 grant 못 하도록 maxClones 0 으로 설정하면
	// 큐가 가득 차서 QueueFullError 가 발생할 수 있다. 결정적이지 않을 수 있어
	// context 취소 경로를 대신 검증한다.
	cc := NewConcurrencyController(&ConcurrencyConfig{
		MaxConcurrentClones: 0, // 절대 grant 안 됨
		MaxConcurrentPushes: 0,
		PerTenantLimit:      0,
		PerIPLimit:          0,
		QueueSize:           1,
	}, NewMetricsCollector("covtest"), zaptest.NewLogger(t))
	defer cc.Stop()

	// 이미 취소된 컨텍스트는 ctx.Err() 를 반환 (대기 경로).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := cc.RequestOperation(ctx, OperationClone, "t", "ip")
	require.Error(t, err)
}

// ==============================
// config.go — 환경변수 override / Validate
// ==============================

func TestApplyEnvironmentOverrides(t *testing.T) {
	// 환경변수로 설정 override 가 적용되는지 확인.
	t.Setenv(EnvHandshakeTimeout, "7s")
	t.Setenv(EnvAuthTimeout, "8s")
	t.Setenv(EnvProtocolNegTimeout, "9s")
	t.Setenv(EnvOperationIdleTimeout, "11s")
	t.Setenv(EnvOperationMaxTimeout, "12m")
	t.Setenv(EnvStallThreshold, "13s")
	t.Setenv(EnvProgressCheckInterval, "14s")
	t.Setenv(EnvMaxConcurrentClones, "77")
	t.Setenv(EnvMaxConcurrentPushes, "33")
	t.Setenv(EnvPerTenantLimit, "9")
	t.Setenv(EnvPerIPLimit, "4")
	t.Setenv(EnvReadBufferSize, "2048")
	t.Setenv(EnvWriteBufferSize, "4096")
	t.Setenv(EnvMetricsEnabled, "false")
	t.Setenv(EnvMetricsNamespace, "custom_ns")
	t.Setenv(EnvHealthCheckInterval, "15s")
	t.Setenv(EnvMaxIdleDuration, "16m")

	cm := NewConfigManager()

	assert.Equal(t, 7*time.Second, cm.Timeouts.Handshake)
	assert.Equal(t, 8*time.Second, cm.Timeouts.Auth)
	assert.Equal(t, 9*time.Second, cm.Timeouts.ProtocolNegotiation)
	assert.Equal(t, 11*time.Second, cm.Timeouts.OperationIdle)
	assert.Equal(t, 12*time.Minute, cm.Timeouts.OperationMax)
	assert.Equal(t, 13*time.Second, cm.Timeouts.StallThreshold)
	assert.Equal(t, 14*time.Second, cm.Timeouts.ProgressCheckInterval)
	assert.Equal(t, 77, cm.Concurrency.MaxConcurrentClones)
	assert.Equal(t, 33, cm.Concurrency.MaxConcurrentPushes)
	assert.Equal(t, 9, cm.Concurrency.PerTenantLimit)
	assert.Equal(t, 4, cm.Concurrency.PerIPLimit)
	assert.Equal(t, 2048, cm.IO.ReadBufferSize)
	assert.Equal(t, 4096, cm.IO.WriteBufferSize)
	assert.False(t, cm.Monitoring.MetricsEnabled)
	assert.Equal(t, "custom_ns", cm.Monitoring.MetricsNamespace)
	assert.Equal(t, 15*time.Second, cm.Monitoring.HealthCheckInterval)
	assert.Equal(t, 16*time.Minute, cm.Monitoring.MaxIdleDuration)
}

func TestApplyEnvironmentOverrides_FastTimeouts(t *testing.T) {
	// FastTimeouts=true 면 testing timeouts 가 적용된다.
	t.Setenv(EnvTestingFastTimeouts, "true")
	cm := NewConfigManager()
	assert.True(t, cm.Testing.FastTimeouts)
	assert.Equal(t, getTestingTimeouts().Handshake, cm.Timeouts.Handshake)
}

func TestApplyEnvironmentOverrides_InvalidValuesIgnored(t *testing.T) {
	// 파싱 실패한 값은 무시되고 기본값이 유지된다.
	t.Setenv(EnvHandshakeTimeout, "not-a-duration")
	t.Setenv(EnvMaxConcurrentClones, "not-a-number")
	cm := NewConfigManager()
	assert.Equal(t, getDefaultTimeouts().Handshake, cm.Timeouts.Handshake)
	assert.Equal(t, getDefaultConcurrencyConfig().MaxConcurrentClones, cm.Concurrency.MaxConcurrentClones)
}

func TestConfigManager_Validate(t *testing.T) {
	// 기본 설정은 유효해야 한다.
	require.NoError(t, NewConfigManager().Validate())

	// 각 위반 케이스를 개별적으로 검증.
	bad := NewConfigManager()
	bad.Timeouts.Handshake = 0
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.Timeouts.OperationMax = bad.Timeouts.OperationIdle // max <= idle
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.Timeouts.StallThreshold = 0
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.IO.ReadBufferSize = 0
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.IO.WriteBufferSize = 0
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.Concurrency.MaxConcurrentClones = 0
	assert.Error(t, bad.Validate())

	bad = NewConfigManager()
	bad.Concurrency.MaxConcurrentPushes = 0
	assert.Error(t, bad.Validate())
}

func TestConfigManager_Converters(t *testing.T) {
	cm := NewConfigManagerFromEnv()
	require.NotNil(t, cm)

	assert.NotNil(t, cm.ToTimeoutPolicy())
	assert.NotNil(t, cm.ToIOWrapperConfig())
	assert.NotNil(t, cm.ToConcurrencyConfig())
	assert.NotEmpty(t, cm.GetSummary())

	// ConcurrencyConfigFromConfig: nil 은 default, non-nil 은 변환.
	assert.NotNil(t, ConcurrencyConfigFromConfig(nil))
	assert.NotNil(t, ConcurrencyConfigFromConfig(cm))

	// TimeoutPolicyFromConfig 동일.
	assert.NotNil(t, TimeoutPolicyFromConfig(nil))
	assert.NotNil(t, TimeoutPolicyFromConfig(cm))

	// 환경 정리 후 testing manager 검증.
	os.Clearenv()
	tm := NewConfigManagerForTesting()
	assert.True(t, tm.Testing.FastTimeouts)
	assert.Equal(t, 1024, tm.IO.ReadBufferSize)
}

// ==============================
// factory.go — GetDefaultPolicy / UpdateDefaultPolicy / CustomConfig
// ==============================

func TestConnectionFactory_PolicyManagement(t *testing.T) {
	logger := zaptest.NewLogger(t)
	factory := NewConnectionFactory(&ConnectionFactoryConfig{Logger: logger}).(*connectionFactory)

	// 기본 정책이 설정되어 있어야 한다.
	policy := factory.GetDefaultPolicy()
	require.NotNil(t, policy)

	// 정책 업데이트.
	newPolicy := DefaultTimeoutPolicy()
	newPolicy.HandshakeTimeout = 99 * time.Second
	factory.UpdateDefaultPolicy(newPolicy)
	assert.Equal(t, 99*time.Second, factory.GetDefaultPolicy().HandshakeTimeout)

	// nil 정책은 무시된다.
	factory.UpdateDefaultPolicy(nil)
	assert.Equal(t, 99*time.Second, factory.GetDefaultPolicy().HandshakeTimeout)
}

func TestConnectionFactory_CreateConnectionWithCustomConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)
	factory := NewConnectionFactory(&ConnectionFactoryConfig{Logger: logger}).(*connectionFactory)
	conn := &MockTCPConn{}

	// nil config → 팩토리 기본값으로 채워짐.
	mgr, err := factory.CreateConnectionWithCustomConfig(context.Background(), conn, nil)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	mgr.Close()

	// 부분 채워진 config → 나머지는 기본값으로 채워짐.
	mgr2, err := factory.CreateConnectionWithCustomConfig(context.Background(), &MockTCPConn{}, &ConnectionManagerConfig{})
	require.NoError(t, err)
	require.NotNil(t, mgr2)
	mgr2.Close()
}

func TestConnectionFactory_CreateTestConnection(t *testing.T) {
	factory := NewConnectionFactory(nil)
	mgr, err := factory.CreateTestConnection(&MockTCPConn{})
	require.NoError(t, err)
	require.NotNil(t, mgr)
	mgr.Close()
}

// ==============================
// progress_aware.go — Write / Flush / Context / getters / SetConnectionInfo
// ==============================

func TestProgressAwareConnection_WriteFlushAndGetters(t *testing.T) {
	logger := zaptest.NewLogger(t)
	conn := &MockTCPConn{}
	policy := DefaultTimeoutPolicy()

	pac := NewProgressAwareConnection(conn, policy, logger)
	defer pac.Close()

	// Write 는 buffered writer 로 들어간다.
	n, err := pac.Write([]byte("hello world"))
	require.NoError(t, err)
	assert.Equal(t, 11, n)

	// Flush 가 버퍼를 실제 conn 으로 보낸다 → progressRecordingWriter 가 progress 기록.
	require.NoError(t, pac.Flush())
	assert.Equal(t, "hello world", string(conn.writeBuffer))

	bytes, _, _ := pac.GetProgressTracker().GetProgress()
	assert.Greater(t, bytes, int64(0))

	// Getters.
	assert.NotNil(t, pac.GetReader())
	assert.NotNil(t, pac.GetWriter())
	assert.NotNil(t, pac.Context())
	assert.NotNil(t, pac.GetRemoteAddr())

	// 메타데이터 set/get.
	pac.SetConnectionInfo("owner", "repo", "user", true)
	owner, repo, userID, protoV2 := pac.GetConnectionInfo()
	assert.Equal(t, "owner", owner)
	assert.Equal(t, "repo", repo)
	assert.Equal(t, "user", userID)
	assert.True(t, protoV2)
}

func TestProgressAwareConnection_GetRemoteAddr_NilConn(t *testing.T) {
	// conn 이 nil 일 때 GetRemoteAddr 은 nil.
	pac := &ProgressAwareConnection{}
	assert.Nil(t, pac.GetRemoteAddr())
}

func TestProgressAwareConnection_OperationsAfterClose(t *testing.T) {
	logger := zaptest.NewLogger(t)
	conn := &MockTCPConn{readBuffer: []byte("data")}
	pac := NewProgressAwareConnection(conn, DefaultTimeoutPolicy(), logger)

	require.NoError(t, pac.Close())
	// 이중 close 는 안전.
	require.NoError(t, pac.Close())

	// 닫힌 후 모든 I/O 는 ConnectionClosedError.
	_, err := pac.Read(make([]byte, 4))
	assert.Error(t, err)
	_, err = pac.Write([]byte("x"))
	assert.Error(t, err)
	assert.Error(t, pac.Flush())
	assert.Error(t, pac.SetPhase(PhaseOperation))

	// IsAlive 는 닫힌 후 false.
	assert.False(t, pac.IsAlive())
}

// ==============================
// io_wrapper.go — SetProgressCallback / IsClosed
// ==============================

func TestIOWrapper_CallbackAndIsClosed(t *testing.T) {
	conn := &MockTCPConn{readBuffer: []byte("payload")}
	// SetProgressCallback / IsClosed 는 구체 타입 *ioWrapper 의 메서드다.
	wrapper := NewIOWrapper(conn, DefaultIOWrapperConfig()).(*ioWrapper)

	assert.False(t, wrapper.IsClosed())

	// 콜백을 교체하고 read 로 트리거.
	var recorded int64
	wrapper.SetProgressCallback(func(b int64) { recorded += b })
	buf := make([]byte, 7)
	_, err := wrapper.Read(buf)
	require.NoError(t, err)
	assert.Greater(t, recorded, int64(0))

	require.NoError(t, wrapper.Close())
	assert.True(t, wrapper.IsClosed())
}

// ==============================
// lifecycle.go — IsIdleTimeout / GetConnectionInfo / GetRemoteAddr nil
// ==============================

func TestConnectionLifecycle_IdleAndInfo(t *testing.T) {
	conn := &MockTCPConn{}
	lc := NewConnectionLifecycle(conn, &ConnectionLifecycleConfig{}).(*connectionLifecycle)

	// 방금 생성됐으므로 큰 idle 임계값으로는 timeout 아님.
	assert.False(t, lc.IsIdleTimeout(time.Hour))
	// 0 임계값으로는 timeout.
	assert.True(t, lc.IsIdleTimeout(0))

	// GetConnectionInfo 는 주소 등 메타 포함.
	info := lc.GetConnectionInfo()
	assert.Contains(t, info, "closed")
	assert.Contains(t, info, "remote_addr")
}

func TestConnectionLifecycle_GetRemoteAddr_NilConn(t *testing.T) {
	lc := &connectionLifecycle{}
	assert.Nil(t, lc.GetRemoteAddr())
}

// ==============================
// manager.go — GetReader / GetWriter / GetRemoteAddr / 메타데이터
// ==============================

func TestConnectionManager_Accessors(t *testing.T) {
	logger := zaptest.NewLogger(t)
	conn := &MockTCPConn{}
	mgr, err := NewConnectionManager(conn, &ConnectionManagerConfig{Logger: logger})
	require.NoError(t, err)
	defer mgr.Close()

	assert.NotNil(t, mgr.GetReader())
	assert.NotNil(t, mgr.GetWriter())
	assert.NotNil(t, mgr.GetRemoteAddr())
	assert.NotNil(t, mgr.GetProgressTracker())

	// 메타데이터 왕복.
	mgr.SetConnectionInfo("o", "r", "u", true)
	o, r, u, p := mgr.GetConnectionInfo()
	assert.Equal(t, "o", o)
	assert.Equal(t, "r", r)
	assert.Equal(t, "u", u)
	assert.True(t, p)

	assert.True(t, mgr.IsAlive())
}

func TestConnectionManager_NilConn(t *testing.T) {
	_, err := NewConnectionManager(nil, nil)
	require.Error(t, err)
}

// ==============================
// metadata.go — GetTenantID 빈 케이스
// ==============================

func TestConnectionMetadata_GetTenantID_Empty(t *testing.T) {
	md := NewConnectionMetadata().(*connectionMetadata)
	// owner/repo 가 비어 있으면 tenant ID 는 "unknown".
	assert.Equal(t, "unknown", md.GetTenantID())

	md.SetConnectionInfo("acme", "widgets", "u", false)
	assert.Equal(t, "acme/widgets", md.GetTenantID())
}

// ==============================
// atomic_push.go — parseRefUpdate 엣지
// ==============================

func TestParseRefUpdate_Edges(t *testing.T) {
	// 빈 입력 → EMPTY 검증 에러.
	_, _, _, err := parseRefUpdate("")
	require.Error(t, err)

	// null 바이트가 있는 입력 (capabilities 분리).
	old, newH, ref, err := parseRefUpdate(
		"0000000000000000000000000000000000000000 1111111111111111111111111111111111111111 refs/heads/main\x00report-status")
	require.NoError(t, err)
	assert.Equal(t, "0000000000000000000000000000000000000000", old)
	assert.Equal(t, "1111111111111111111111111111111111111111", newH)
	assert.Equal(t, "refs/heads/main", ref)

	// 필드 부족 (3개 미만) → INVALID_FORMAT 에러.
	_, _, _, err = parseRefUpdate("onlyonefield")
	require.Error(t, err)

	// findNull: null 없음 → -1.
	assert.Equal(t, -1, findNull("no null here"))
	assert.Equal(t, 3, findNull("abc\x00def"))
}

func TestAtomicPushHandler_PrepareInvalidRef(t *testing.T) {
	logger := zaptest.NewLogger(t)
	handler := NewAtomicPushHandler("", "op-x", logger)

	// 파싱 불가능한 ref update 는 WrapError 로 검증 에러.
	err := handler.PreparePushOperation([]string{"bad"})
	require.Error(t, err)
	assert.Contains(t, fmt.Sprint(err), "failed to parse ref update")
}
