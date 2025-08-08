package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// TLSConfig holds configuration for TLS/HTTPS
type TLSConfig struct {
	Enabled             bool               `yaml:"enabled" json:"enabled"`
	CertFile            string             `yaml:"cert_file" json:"cert_file"`
	KeyFile             string             `yaml:"key_file" json:"key_file"`
	CAFile              string             `yaml:"ca_file" json:"ca_file"`
	MinVersion          string             `yaml:"min_version" json:"min_version"`     // TLS 1.2, 1.3
	MaxVersion          string             `yaml:"max_version" json:"max_version"`     // TLS 1.2, 1.3
	CipherSuites        []string           `yaml:"cipher_suites" json:"cipher_suites"` // Allowed cipher suites
	PreferServerCiphers bool               `yaml:"prefer_server_ciphers" json:"prefer_server_ciphers"`
	ClientAuth          string             `yaml:"client_auth" json:"client_auth"` // none, request, require, verify
	InsecureSkipVerify  bool               `yaml:"insecure_skip_verify" json:"insecure_skip_verify"`
	ServerName          string             `yaml:"server_name" json:"server_name"` // For SNI
	NextProtos          []string           `yaml:"next_protos" json:"next_protos"` // ALPN protocols
	HSTS                HSTSConfig         `yaml:"hsts" json:"hsts"`               // HTTP Strict Transport Security
	CertificateRotation CertRotationConfig `yaml:"certificate_rotation" json:"certificate_rotation"`
	AutoCert            AutoCertConfig     `yaml:"auto_cert" json:"auto_cert"` // Let's Encrypt auto-cert
}

// HSTSConfig configures HTTP Strict Transport Security
type HSTSConfig struct {
	Enabled           bool  `yaml:"enabled" json:"enabled"`
	MaxAge            int64 `yaml:"max_age" json:"max_age"` // In seconds
	IncludeSubdomains bool  `yaml:"include_subdomains" json:"include_subdomains"`
	Preload           bool  `yaml:"preload" json:"preload"`
}

// CertRotationConfig configures automatic certificate rotation
type CertRotationConfig struct {
	Enabled          bool          `yaml:"enabled" json:"enabled"`
	CheckInterval    time.Duration `yaml:"check_interval" json:"check_interval"`
	RenewalThreshold time.Duration `yaml:"renewal_threshold" json:"renewal_threshold"` // Renew when cert expires within this time
	BackupCerts      bool          `yaml:"backup_certs" json:"backup_certs"`
}

// AutoCertConfig configures Let's Encrypt automatic certificates
type AutoCertConfig struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	Hosts        []string `yaml:"hosts" json:"hosts"`                 // Allowed hostnames
	CacheDir     string   `yaml:"cache_dir" json:"cache_dir"`         // Directory to cache certificates
	Email        string   `yaml:"email" json:"email"`                 // Contact email for Let's Encrypt
	DirectoryURL string   `yaml:"directory_url" json:"directory_url"` // ACME directory URL
	Staging      bool     `yaml:"staging" json:"staging"`             // Use Let's Encrypt staging
}

// TLSManager manages TLS configuration and certificates
type TLSManager struct {
	config    *TLSConfig
	logger    *logging.Logger
	tlsConfig *tls.Config
}

// DefaultTLSConfig returns secure TLS defaults
func DefaultTLSConfig() *TLSConfig {
	return &TLSConfig{
		Enabled:             true,
		CertFile:            "/etc/git-executor/tls.crt",
		KeyFile:             "/etc/git-executor/tls.key",
		CAFile:              "",
		MinVersion:          "1.2",
		MaxVersion:          "1.3",
		PreferServerCiphers: true,
		ClientAuth:          "none",
		InsecureSkipVerify:  false,
		NextProtos:          []string{"h2", "http/1.1"},
		HSTS: HSTSConfig{
			Enabled:           true,
			MaxAge:            31536000, // 1 year
			IncludeSubdomains: true,
			Preload:           false,
		},
		CertificateRotation: CertRotationConfig{
			Enabled:          false,
			CheckInterval:    24 * time.Hour,
			RenewalThreshold: 30 * 24 * time.Hour, // 30 days
			BackupCerts:      true,
		},
		AutoCert: AutoCertConfig{
			Enabled:      false,
			CacheDir:     "/etc/git-executor/autocert",
			DirectoryURL: "", // Use default (production)
			Staging:      false,
		},
		CipherSuites: []string{
			"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
			"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
			"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
			"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305",
		},
	}
}

// NewTLSManager creates a new TLS manager
func NewTLSManager(config *TLSConfig, logger *logging.Logger) (*TLSManager, error) {
	if config == nil {
		config = DefaultTLSConfig()
	}

	tm := &TLSManager{
		config: config,
		logger: logger,
	}

	if config.Enabled {
		tlsConfig, err := tm.buildTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build TLS config: %w", err)
		}
		tm.tlsConfig = tlsConfig
	}

	return tm, nil
}

// GetTLSConfig returns the configured tls.Config
func (tm *TLSManager) GetTLSConfig() *tls.Config {
	return tm.tlsConfig
}

// GetHTTPSServer returns an HTTP server configured with TLS
func (tm *TLSManager) GetHTTPSServer(handler http.Handler, addr string) *http.Server {
	server := &http.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: tm.tlsConfig,
		// Security timeouts
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Add HSTS middleware if enabled
	if tm.config.HSTS.Enabled {
		server.Handler = tm.hstsMiddleware(handler)
	}

	return server
}

// buildTLSConfig constructs the tls.Config from our configuration
func (tm *TLSManager) buildTLSConfig() (*tls.Config, error) {
	config := &tls.Config{
		PreferServerCipherSuites: tm.config.PreferServerCiphers,
		NextProtos:               tm.config.NextProtos,
		ServerName:               tm.config.ServerName,
		InsecureSkipVerify:       tm.config.InsecureSkipVerify,
	}

	// Set TLS version range
	if err := tm.setTLSVersions(config); err != nil {
		return nil, fmt.Errorf("failed to set TLS versions: %w", err)
	}

	// Set cipher suites
	if err := tm.setCipherSuites(config); err != nil {
		return nil, fmt.Errorf("failed to set cipher suites: %w", err)
	}

	// Set client authentication mode
	if err := tm.setClientAuth(config); err != nil {
		return nil, fmt.Errorf("failed to set client auth: %w", err)
	}

	// Load certificates
	if tm.config.CertFile != "" && tm.config.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tm.config.CertFile, tm.config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}

		tm.logger.Info("TLS certificate loaded",
			zap.String("cert_file", tm.config.CertFile),
			zap.String("key_file", tm.config.KeyFile))
	}

	// Load CA certificates if specified
	if tm.config.CAFile != "" {
		if err := tm.loadCAFile(config); err != nil {
			return nil, fmt.Errorf("failed to load CA file: %w", err)
		}
	}

	return config, nil
}

// setTLSVersions configures the minimum and maximum TLS versions
func (tm *TLSManager) setTLSVersions(config *tls.Config) error {
	versionMap := map[string]uint16{
		"1.0": tls.VersionTLS10,
		"1.1": tls.VersionTLS11,
		"1.2": tls.VersionTLS12,
		"1.3": tls.VersionTLS13,
	}

	if tm.config.MinVersion != "" {
		minVer, ok := versionMap[tm.config.MinVersion]
		if !ok {
			return fmt.Errorf("invalid TLS min version: %s", tm.config.MinVersion)
		}
		config.MinVersion = minVer
	} else {
		config.MinVersion = tls.VersionTLS12 // Secure default
	}

	if tm.config.MaxVersion != "" {
		maxVer, ok := versionMap[tm.config.MaxVersion]
		if !ok {
			return fmt.Errorf("invalid TLS max version: %s", tm.config.MaxVersion)
		}
		config.MaxVersion = maxVer
	}

	tm.logger.Info("TLS version range configured",
		zap.String("min_version", tm.config.MinVersion),
		zap.String("max_version", tm.config.MaxVersion))

	return nil
}

// setCipherSuites configures the allowed cipher suites
func (tm *TLSManager) setCipherSuites(config *tls.Config) error {
	if len(tm.config.CipherSuites) == 0 {
		// Use secure defaults
		return nil
	}

	cipherMap := map[string]uint16{
		"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384":   tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256":   tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305":    tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384": tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256": tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305":  tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		"TLS_RSA_WITH_AES_256_GCM_SHA384":         tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_RSA_WITH_AES_128_GCM_SHA256":         tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	}

	var ciphers []uint16
	for _, cipherName := range tm.config.CipherSuites {
		cipher, ok := cipherMap[cipherName]
		if !ok {
			return fmt.Errorf("unsupported cipher suite: %s", cipherName)
		}
		ciphers = append(ciphers, cipher)
	}

	config.CipherSuites = ciphers
	tm.logger.Info("TLS cipher suites configured", zap.Strings("ciphers", tm.config.CipherSuites))

	return nil
}

// setClientAuth configures client certificate authentication
func (tm *TLSManager) setClientAuth(config *tls.Config) error {
	authMap := map[string]tls.ClientAuthType{
		"none":    tls.NoClientCert,
		"request": tls.RequestClientCert,
		"require": tls.RequireAnyClientCert,
		"verify":  tls.RequireAndVerifyClientCert,
	}

	authType, ok := authMap[strings.ToLower(tm.config.ClientAuth)]
	if !ok {
		return fmt.Errorf("invalid client auth type: %s", tm.config.ClientAuth)
	}

	config.ClientAuth = authType
	tm.logger.Info("TLS client authentication configured", zap.String("auth_type", tm.config.ClientAuth))

	return nil
}

// loadCAFile loads CA certificates for client verification
func (tm *TLSManager) loadCAFile(config *tls.Config) error {
	caPEM, err := os.ReadFile(tm.config.CAFile)
	if err != nil {
		return fmt.Errorf("failed to read CA file: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("failed to parse CA certificates")
	}

	config.ClientCAs = caCertPool
	tm.logger.Info("CA certificates loaded", zap.String("ca_file", tm.config.CAFile))

	return nil
}

// hstsMiddleware adds HTTP Strict Transport Security headers
func (tm *TLSManager) hstsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil { // Only add HSTS for HTTPS requests
			hstsValue := fmt.Sprintf("max-age=%d", tm.config.HSTS.MaxAge)

			if tm.config.HSTS.IncludeSubdomains {
				hstsValue += "; includeSubDomains"
			}

			if tm.config.HSTS.Preload {
				hstsValue += "; preload"
			}

			w.Header().Set("Strict-Transport-Security", hstsValue)
		}

		next.ServeHTTP(w, r)
	})
}

// ValidateCertificate validates that certificates exist and are valid
func (tm *TLSManager) ValidateCertificate() error {
	if !tm.config.Enabled {
		return nil
	}

	if tm.config.CertFile == "" || tm.config.KeyFile == "" {
		return fmt.Errorf("certificate and key files must be specified when TLS is enabled")
	}

	// Check if files exist
	if _, err := os.Stat(tm.config.CertFile); os.IsNotExist(err) {
		return fmt.Errorf("certificate file does not exist: %s", tm.config.CertFile)
	}

	if _, err := os.Stat(tm.config.KeyFile); os.IsNotExist(err) {
		return fmt.Errorf("key file does not exist: %s", tm.config.KeyFile)
	}

	// Try to load the certificate to validate it
	cert, err := tls.LoadX509KeyPair(tm.config.CertFile, tm.config.KeyFile)
	if err != nil {
		return fmt.Errorf("failed to load certificate pair: %w", err)
	}

	// Parse the certificate to check expiration
	if len(cert.Certificate) > 0 {
		parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("failed to parse certificate: %w", err)
		}

		// Check if certificate is expired
		now := time.Now()
		if now.After(parsedCert.NotAfter) {
			return fmt.Errorf("certificate has expired on %s", parsedCert.NotAfter)
		}

		// Warn if certificate expires soon
		if now.Add(30 * 24 * time.Hour).After(parsedCert.NotAfter) {
			tm.logger.Warn("Certificate expires soon",
				zap.Time("expiry", parsedCert.NotAfter),
				zap.Duration("time_remaining", time.Until(parsedCert.NotAfter)))
		}

		tm.logger.Info("Certificate validation successful",
			zap.String("subject", parsedCert.Subject.String()),
			zap.Time("not_before", parsedCert.NotBefore),
			zap.Time("not_after", parsedCert.NotAfter))
	}

	return nil
}

// GetCertificateInfo returns information about the loaded certificate
func (tm *TLSManager) GetCertificateInfo() (map[string]interface{}, error) {
	if !tm.config.Enabled || tm.config.CertFile == "" {
		return map[string]interface{}{
			"enabled": false,
		}, nil
	}

	cert, err := tls.LoadX509KeyPair(tm.config.CertFile, tm.config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	info := map[string]interface{}{
		"enabled":   true,
		"cert_file": tm.config.CertFile,
		"key_file":  tm.config.KeyFile,
	}

	if len(cert.Certificate) > 0 {
		parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil {
			info["subject"] = parsedCert.Subject.String()
			info["issuer"] = parsedCert.Issuer.String()
			info["not_before"] = parsedCert.NotBefore
			info["not_after"] = parsedCert.NotAfter
			info["dns_names"] = parsedCert.DNSNames
			info["ip_addresses"] = parsedCert.IPAddresses
			info["serial_number"] = parsedCert.SerialNumber.String()
			info["is_ca"] = parsedCert.IsCA
			info["signature_algorithm"] = parsedCert.SignatureAlgorithm.String()
			info["public_key_algorithm"] = parsedCert.PublicKeyAlgorithm.String()

			// Calculate days until expiry
			daysUntilExpiry := time.Until(parsedCert.NotAfter).Hours() / 24
			info["days_until_expiry"] = int(daysUntilExpiry)
			info["expired"] = time.Now().After(parsedCert.NotAfter)
		}
	}

	return info, nil
}

// RedirectToHTTPS returns a handler that redirects HTTP to HTTPS
func RedirectToHTTPS(httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			// Already HTTPS, continue normally
			return
		}

		// Construct HTTPS URL
		host := r.Host
		if httpsPort != "443" && httpsPort != "" {
			// Remove existing port if present
			if colonIndex := strings.LastIndex(host, ":"); colonIndex != -1 {
				host = host[:colonIndex]
			}
			host = host + ":" + httpsPort
		}

		httpsURL := "https://" + host + r.RequestURI

		// Use 301 (permanent redirect) for security
		http.Redirect(w, r, httpsURL, http.StatusMovedPermanently)
	}
}

// SecurityHeaders adds common security headers
func SecurityHeaders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Enable XSS protection
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Prevent page from being displayed in frames
		w.Header().Set("X-Frame-Options", "DENY")

		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Content Security Policy (basic)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")

		// Permissions policy
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
	}
}
