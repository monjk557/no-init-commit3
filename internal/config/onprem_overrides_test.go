package config

import (
	"testing"
)

// TestApplyOnpremOverrides_GCS_ForcedToLocal는 onprem 환경에서
// LFS_STORAGE_BACKEND=gcs 가 local 로 강제 변경됨을 검증한다.
// onprem 은 air-gap 환경이라 storage.googleapis.com 도달 불가능 — 의도된 동작.
func TestApplyOnpremOverrides_GCS_ForcedToLocal(t *testing.T) {
	cfg := &Config{
		LFS: LFSConfig{
			Enabled:        true,
			StorageBackend: "gcs",
			GCSBucket:      "some-bucket",
			GCSProject:     "some-project",
		},
	}

	applyOnpremOverrides(cfg)

	if cfg.LFS.StorageBackend != "local" {
		t.Errorf("expected StorageBackend=local, got %q", cfg.LFS.StorageBackend)
	}
	if cfg.LFS.GCSBucket != "" {
		t.Errorf("expected GCSBucket to be cleared, got %q", cfg.LFS.GCSBucket)
	}
	if cfg.LFS.GCSProject != "" {
		t.Errorf("expected GCSProject to be cleared, got %q", cfg.LFS.GCSProject)
	}
}

// TestApplyOnpremOverrides_LFSDisabled_NoChange는 LFS_ENABLED=false 일 때는
// GCS backend 라도 override 가 일어나지 않음을 검증 (불필요한 변경 회피).
func TestApplyOnpremOverrides_LFSDisabled_NoChange(t *testing.T) {
	cfg := &Config{
		LFS: LFSConfig{
			Enabled:        false,
			StorageBackend: "gcs",
			GCSBucket:      "some-bucket",
			GCSProject:     "some-project",
		},
	}

	applyOnpremOverrides(cfg)

	if cfg.LFS.StorageBackend != "gcs" {
		t.Errorf("expected StorageBackend=gcs (unchanged), got %q", cfg.LFS.StorageBackend)
	}
	if cfg.LFS.GCSBucket != "some-bucket" {
		t.Errorf("expected GCSBucket unchanged, got %q", cfg.LFS.GCSBucket)
	}
}

// TestApplyOnpremOverrides_LocalBackend_NoChange는 이미 local backend 인 경우
// override 가 no-op 임을 검증.
func TestApplyOnpremOverrides_LocalBackend_NoChange(t *testing.T) {
	cfg := &Config{
		LFS: LFSConfig{
			Enabled:        true,
			StorageBackend: "local",
			LocalPath:      "/var/lib/lfs",
		},
	}

	applyOnpremOverrides(cfg)

	if cfg.LFS.StorageBackend != "local" {
		t.Errorf("expected StorageBackend=local (unchanged), got %q", cfg.LFS.StorageBackend)
	}
	if cfg.LFS.LocalPath != "/var/lib/lfs" {
		t.Errorf("expected LocalPath unchanged, got %q", cfg.LFS.LocalPath)
	}
}

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
