package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func tlsTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	return &logging.Logger{Logger: zaptest.NewLogger(t)}
}

// writeSelfSignedCert 는 임시 디렉터리에 자체 서명 ECDSA 인증서/키 쌍을 만들어 경로를 반환한다.
func writeSelfSignedCert(t *testing.T, notAfter time.Time) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test.local", Organization: []string{"CollabOps Test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"test.local"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	certOut, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certOut.Close())

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	keyOut, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyOut.Close())

	return certPath, keyPath
}

// --- DefaultTLSConfig ---

func Test_DefaultTLSConfig(t *testing.T) {
	cfg := DefaultTLSConfig()
	require.True(t, cfg.Enabled)
	require.Equal(t, "1.2", cfg.MinVersion)
	require.Equal(t, "1.3", cfg.MaxVersion)
	require.NotEmpty(t, cfg.CipherSuites)
	require.True(t, cfg.HSTS.Enabled)
}

// --- NewTLSManager ---

// Test_NewTLSManager_Disabled_NoTLSConfig Enabled=false 시 tlsConfig nil
func Test_NewTLSManager_Disabled_NoTLSConfig(t *testing.T) {
	cfg := &TLSConfig{Enabled: false}
	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	require.Nil(t, tm.GetTLSConfig())
}

// Test_NewTLSManager_NilConfig_DefaultsButCertMissing nil config → 기본값, 기본 cert 경로 없으면 에러
func Test_NewTLSManager_NilConfig_DefaultsButCertMissing(t *testing.T) {
	// 기본 config 는 /etc/git-executor/tls.crt 를 가리켜 로컬에 없으므로 빌드 실패
	tm, err := NewTLSManager(nil, tlsTestLogger(t))
	require.Error(t, err)
	require.Nil(t, tm)
}

// Test_NewTLSManager_ValidCert 정상 인증서로 매니저 생성
func Test_NewTLSManager_ValidCert(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(365*24*time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CAFile = ""

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	tlsCfg := tm.GetTLSConfig()
	require.NotNil(t, tlsCfg)
	require.Len(t, tlsCfg.Certificates, 1)
	require.Equal(t, uint16(tls.VersionTLS12), tlsCfg.MinVersion)
	require.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MaxVersion)
}

// Test_NewTLSManager_MissingCertFile 존재하지 않는 cert 경로 → 에러
func Test_NewTLSManager_MissingCertFile(t *testing.T) {
	cfg := DefaultTLSConfig()
	cfg.CertFile = filepath.Join(t.TempDir(), "nope.crt")
	cfg.KeyFile = filepath.Join(t.TempDir(), "nope.key")

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
	require.Nil(t, tm)
}

// --- setTLSVersions (buildTLSConfig 경유) ---

func Test_BuildTLSConfig_InvalidMinVersion(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.MinVersion = "9.9"

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

func Test_BuildTLSConfig_InvalidMaxVersion(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.MaxVersion = "bogus"

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

func Test_BuildTLSConfig_EmptyMinVersion_DefaultsTo12(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.MinVersion = ""
	cfg.MaxVersion = ""

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS12), tm.GetTLSConfig().MinVersion)
}

// --- setCipherSuites ---

func Test_BuildTLSConfig_CustomCiphers(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CipherSuites = []string{
		"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
		"TLS_RSA_WITH_AES_128_GCM_SHA256",
	}

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	require.Len(t, tm.GetTLSConfig().CipherSuites, 2)
}

func Test_BuildTLSConfig_UnsupportedCipher(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CipherSuites = []string{"TLS_MADE_UP_CIPHER"}

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

func Test_BuildTLSConfig_EmptyCiphers_UsesDefaults(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CipherSuites = nil

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	require.Nil(t, tm.GetTLSConfig().CipherSuites) // 기본값 → 명시 설정 없음
}

// --- setClientAuth ---

func Test_BuildTLSConfig_ClientAuthModes(t *testing.T) {
	cases := map[string]tls.ClientAuthType{
		"none":    tls.NoClientCert,
		"request": tls.RequestClientCert,
		"require": tls.RequireAnyClientCert,
		"verify":  tls.RequireAndVerifyClientCert,
		"VERIFY":  tls.RequireAndVerifyClientCert, // 대소문자 무시
	}
	for mode, want := range cases {
		t.Run(mode, func(t *testing.T) {
			certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
			cfg := DefaultTLSConfig()
			cfg.CertFile = certPath
			cfg.KeyFile = keyPath
			cfg.ClientAuth = mode
			tm, err := NewTLSManager(cfg, tlsTestLogger(t))
			require.NoError(t, err)
			require.Equal(t, want, tm.GetTLSConfig().ClientAuth)
		})
	}
}

func Test_BuildTLSConfig_InvalidClientAuth(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.ClientAuth = "bogus"

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

// --- loadCAFile ---

func Test_BuildTLSConfig_ValidCAFile(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	// 자체 서명 인증서를 CA 로 재사용
	cfg.CAFile = certPath

	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)
	require.NotNil(t, tm.GetTLSConfig().ClientCAs)
}

func Test_BuildTLSConfig_MissingCAFile(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CAFile = filepath.Join(t.TempDir(), "missing-ca.pem")

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

func Test_BuildTLSConfig_InvalidCAFileContent(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	badCA := filepath.Join(t.TempDir(), "bad-ca.pem")
	require.NoError(t, os.WriteFile(badCA, []byte("not a pem cert"), 0o600))

	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.CAFile = badCA

	_, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.Error(t, err)
}

// --- GetHTTPSServer + hstsMiddleware ---

func Test_GetHTTPSServer_WithHSTS(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.HSTS.Enabled = true
	cfg.HSTS.IncludeSubdomains = true
	cfg.HSTS.Preload = true
	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := tm.GetHTTPSServer(base, ":8443")
	require.Equal(t, ":8443", srv.Addr)
	require.NotNil(t, srv.TLSConfig)

	// HSTS 미들웨어: TLS 연결일 때만 헤더 추가
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://test.local/", nil)
	req.TLS = &tls.ConnectionState{}
	srv.Handler.ServeHTTP(rec, req)
	hsts := rec.Header().Get("Strict-Transport-Security")
	require.Contains(t, hsts, "max-age=")
	require.Contains(t, hsts, "includeSubDomains")
	require.Contains(t, hsts, "preload")
}

func Test_GetHTTPSServer_HSTS_NoTLS_NoHeader(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := tm.GetHTTPSServer(base, ":8443")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://test.local/", nil) // r.TLS == nil
	srv.Handler.ServeHTTP(rec, req)
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
}

func Test_GetHTTPSServer_HSTSDisabled_UsesRawHandler(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(time.Hour))
	cfg := DefaultTLSConfig()
	cfg.CertFile = certPath
	cfg.KeyFile = keyPath
	cfg.HSTS.Enabled = false
	tm, err := NewTLSManager(cfg, tlsTestLogger(t))
	require.NoError(t, err)

	called := false
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })
	srv := tm.GetHTTPSServer(base, ":8443")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://test.local/", nil)
	req.TLS = &tls.ConnectionState{}
	srv.Handler.ServeHTTP(rec, req)
	require.True(t, called)
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
}

// --- ValidateCertificate ---

func Test_ValidateCertificate_Disabled_NoError(t *testing.T) {
	tm, err := NewTLSManager(&TLSConfig{Enabled: false}, tlsTestLogger(t))
	require.NoError(t, err)
	require.NoError(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_MissingPaths(t *testing.T) {
	tm := &TLSManager{config: &TLSConfig{Enabled: true}, logger: tlsTestLogger(t)}
	require.Error(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_NonexistentFile(t *testing.T) {
	tm := &TLSManager{
		config: &TLSConfig{
			Enabled:  true,
			CertFile: filepath.Join(t.TempDir(), "no.crt"),
			KeyFile:  filepath.Join(t.TempDir(), "no.key"),
		},
		logger: tlsTestLogger(t),
	}
	require.Error(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_ValidCert(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(365*24*time.Hour))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	require.NoError(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_ExpiringSoon_Warns(t *testing.T) {
	// 10일 후 만료 → 30일 임계치 안 → 경고 분기 실행, 에러는 아님
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(10*24*time.Hour))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	require.NoError(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_Expired(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(-time.Hour))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	require.Error(t, tm.ValidateCertificate())
}

func Test_ValidateCertificate_InvalidKeyPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "bad.crt")
	keyPath := filepath.Join(dir, "bad.key")
	require.NoError(t, os.WriteFile(certPath, []byte("garbage"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("garbage"), 0o600))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	require.Error(t, tm.ValidateCertificate())
}

// --- GetCertificateInfo ---

func Test_GetCertificateInfo_Disabled(t *testing.T) {
	tm := &TLSManager{config: &TLSConfig{Enabled: false}, logger: tlsTestLogger(t)}
	info, err := tm.GetCertificateInfo()
	require.NoError(t, err)
	require.Equal(t, false, info["enabled"])
}

func Test_GetCertificateInfo_ValidCert(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(365*24*time.Hour))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	info, err := tm.GetCertificateInfo()
	require.NoError(t, err)
	require.Equal(t, true, info["enabled"])
	require.Contains(t, info, "subject")
	require.Contains(t, info, "not_after")
	require.Equal(t, false, info["expired"])
	require.Contains(t, info["dns_names"], "test.local")
}

func Test_GetCertificateInfo_LoadError(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "bad.crt")
	keyPath := filepath.Join(dir, "bad.key")
	require.NoError(t, os.WriteFile(certPath, []byte("garbage"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("garbage"), 0o600))
	tm := &TLSManager{
		config: &TLSConfig{Enabled: true, CertFile: certPath, KeyFile: keyPath},
		logger: tlsTestLogger(t),
	}
	_, err := tm.GetCertificateInfo()
	require.Error(t, err)
}

// --- RedirectToHTTPS ---

func Test_RedirectToHTTPS(t *testing.T) {
	// 이미 HTTPS → 리다이렉트 없음
	h := RedirectToHTTPS("443")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://example.com/foo", nil)
	req.TLS = &tls.ConnectionState{}
	h(rec, req)
	require.Empty(t, rec.Header().Get("Location"))

	// HTTP, 기본 포트 → https 리다이렉트 (Host 그대로)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://example.com/foo?x=1", nil)
	req.Host = "example.com"
	req.RequestURI = "/foo?x=1"
	h(rec, req)
	require.Equal(t, http.StatusMovedPermanently, rec.Code)
	require.Equal(t, "https://example.com/foo?x=1", rec.Header().Get("Location"))
}

func Test_RedirectToHTTPS_CustomPort(t *testing.T) {
	h := RedirectToHTTPS("8443")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com:80/bar", nil)
	req.Host = "example.com:80"
	req.RequestURI = "/bar"
	h(rec, req)
	require.Equal(t, http.StatusMovedPermanently, rec.Code)
	require.Equal(t, "https://example.com:8443/bar", rec.Header().Get("Location"))
}

// --- SecurityHeaders ---

func Test_SecurityHeaders(t *testing.T) {
	h := SecurityHeaders()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h(rec, req)

	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "1; mode=block", rec.Header().Get("X-XSS-Protection"))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.NotEmpty(t, rec.Header().Get("Referrer-Policy"))
	require.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
	require.NotEmpty(t, rec.Header().Get("Permissions-Policy"))
}
