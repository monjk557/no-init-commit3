package config

import (
	"testing"
)

// TestApplyOnpremOverrides_DisablesEnableDocs는 onprem 에서 API 문서가
// 강제 비활성화됨을 검증 (운영 환경 보안).
func TestApplyOnpremOverrides_DisablesEnableDocs(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{EnableDocs: true},
	}

	applyOnpremOverrides(cfg)

	if cfg.Server.EnableDocs {
		t.Error("expected EnableDocs to be false after onprem override")
	}
}

// TestApplyOnpremOverrides_DisablesTracingEndpoint는 onprem 에서 외부
// Observability exporter 가 비활성화됨을 검증 (내부망 제약).
func TestApplyOnpremOverrides_DisablesTracingEndpoint(t *testing.T) {
	cfg := &Config{
		Observability: ObservabilityConfig{
			Tracing: TracingConfig{Endpoint: "https://tracing.example.com"},
		},
	}

	applyOnpremOverrides(cfg)

	if cfg.Observability.Tracing.Endpoint != "" {
		t.Errorf("expected Tracing.Endpoint to be cleared, got %q", cfg.Observability.Tracing.Endpoint)
	}
}
