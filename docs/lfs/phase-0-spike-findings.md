# Phase 0 Spike — Findings (Issue-2355)

**Date**: 2026-05-11
**Scope**: LFS production-grade rollout track 의 Phase 1 시작 전 3가지 잠재 blocker 검증.
**관련 설계 문서**: [06_Git File Storage, LFS](https://docs.google.com/document/d/1Sm1jJjjpaNDADp0QksvZDSDuYeCXxFk6ORg-rOYX8hw/edit)

---

## 1. obj.stage.collabops.ai 도달성

### 검증 항목
git-executor pod 에서 외부 도메인 `obj.stage.collabops.ai` / cluster-internal MinIO 서비스 양쪽 호출 가능한지 (istio egress 정책).

### 결과: ✅ OK — 양쪽 경로 모두 작동

**근거**:

1. **istio VirtualService 가 외부 host 를 cluster-internal svc 로 routing** —
   `collabops-infrastructure/helm-charts/values/istio/values-onprem-stage.yaml:245-263`:
   ```yaml
   hosts:
     - "obj.stage.collabops.ai"
   gateways:
     - istio-system/collabops-gateway
   http:
     routes:
       - match: { uri: { prefix: / } }
         destination:
           host: collabops-service-objstore.collabops-onprem-stage.svc.cluster.local
           port: 9000
   ```
   외부 도메인 으로 들어온 요청은 istio gateway → VirtualService → cluster-internal svc 로 forwarding.

2. **api-piper 가 이미 동일 패턴 사용 중** (검증된 reference) —
   `helm-charts/values/collabops-api-piper/values-onprem-stage.yaml`:
   - `STORAGE_ENDPOINT: obj.stage.collabops.ai` (presign signing)
   - `CDN_ENDPOINT: https://obj.stage.collabops.ai` (browser-facing)

3. **순수 onprem (air-gap) 은 외부 host 없음** — `values-onprem.yaml` 의 piper:
   - `CDN_ENDPOINT: http://collabops-service-objstore.collabops.svc.cluster.local:9000` (cluster-internal 만)

   → 순수 onprem 환경에는 외부 도메인 가정 불가. LFS 의 `LFS_BASE_URL` 환경별로 다르게 셋업해야 함:
   - stage / onprem-stage: `https://obj.stage.collabops.ai`
   - onprem-skt: 해당 환경의 외부 도메인 (`obj.<customer>.<domain>` 형태, 별도 확인 필요)
   - 순수 onprem (off-domain): 외부 도메인 없으면 client 가 cluster-internal svc 에 도달할 수 있어야 함 (예: VPN, port-forward) — 별도 결정 필요

### Phase 1 영향
- Phase 1.4 (helm values) 의 `LFS_BASE_URL` 은 **환경별 다른 값** 필요 — values-stage / values-onprem-stage / values-onprem-skt / values-onprem 각각 셋업.
- **onprem-skt 활성 시점에 추가 spike 필요** — 해당 환경에 외부 도메인이 있는지 / 어떤 호스트인지 확인.

### 후속 액션
- [ ] onprem-skt 의 `obj.*` 도메인 / 인증서 확인 (Phase 4 rollout 전)
- [ ] 순수 onprem 의 LFS 사용성 정책 결정 (cluster-internal 만으로 client 접근 가능한지)

---

## 2. GCS force-disable 사유

### 검증 항목
`internal/config/config.go:336-340` 의 GCS 강제 비활성 로직이 의도된 비활성인지 미완성인지 판별.

### 결과: ✅ 의도된 비활성 — 단 **글로벌 강제 아니라 onprem 환경 전용**

**근거**:

원본 commit: `cecc383` "Squash merge branch 'fix/app-env-onprem'" (monjk557, 2026-04-10)

전체 함수 컨텍스트:
```go
// applyOnpremOverrides는 onprem 환경에서 필요한 설정을 강제 적용합니다.
// onprem = prod 수준의 보안/안정성 + 내부망 제약 (HTTPS 없음, 외부 서비스 접근 불가, 자체 인프라)
func applyOnpremOverrides(config *Config) {
    config.Server.EnableDocs = false

    // LFS 스토리지: GCS 사용 불가 → local 강제
    if config.LFS.Enabled && config.LFS.StorageBackend == "gcs" {
        config.LFS.StorageBackend = "local"
        config.LFS.GCSBucket = ""
        config.LFS.GCSProject = ""
    }

    config.Observability.Tracing.Endpoint = ""
}
```

호출 컨디션:
```go
if appEnv.IsOnprem() {
    applyOnpremOverrides(config)
}
```

즉 **`LFS_STORAGE_BACKEND=gcs` 는 stage / prod 에서 정상 동작**. onprem 에서만 `local` 로 fallback. air-gap 환경에서 GCS API endpoint 도달 불가능하므로 정당한 처리.

### Phase 1 영향 — Issue-2358 스코프 변경

| 원안 (잘못 진단됨) | 수정안 |
|---|---|
| GCS force-disable 제거 또는 명시적 처리로 변경 | **유지** — onprem override 는 옳음 |
| backend selector 정상화 | `minio` case 추가 + `applyOnpremOverrides` 에 minio 처리 추가 검토 |
| (없음) | 주석 보강 — "**onprem 전용** GCS → local fallback" 명시 |

**핵심 결정**: onprem 환경에서 `minio` backend 는 어떻게 처리?
- 옵션 A: `minio` 도 onprem 에서 그대로 사용 (objstore 가 onprem 에 deploy 되어 있으니 OK) — **권장**
- 옵션 B: `minio` 도 onprem 에서 `local` 로 fallback (보수적)

**권장**: 옵션 A. `collabops-service-objstore` 가 onprem-stage / onprem-skt / onprem (general) 에 이미 deploy 되어 있어 cluster-internal 도달 가능. override 불필요.

### 후속 액션
- [ ] Issue-2358 본문 업데이트 — 스코프 축소 (제거 → 주석 보강 + minio case 추가만)
- [ ] `applyOnpremOverrides` 에 minio 케이스 명시적으로 건드리지 않음 — 위 결정 반영

---

## 3. Presigned URL host 검증

### 검증 항목
MinIO SDK (minio-go/v7) 가 sign 한 URL 의 host 가 cluster-internal 이 아니라 외부 도메인 (`obj.stage.collabops.ai`) 으로 나가는지. 안 되면 dual-client 패턴의 분기점 결정.

### 결과: ⚠️ Dual-client 패턴 필수

**근거**:

minio-go/v7 의 `PresignedPutObject` / `PresignedGetObject` 는 **client 의 endpoint 로 sign**. 즉:
```go
client := minio.New("obj.stage.collabops.ai", &minio.Options{Secure: true, ...})
url, _ := client.PresignedPutObject(ctx, bucket, key, expiry)
// url = https://obj.stage.collabops.ai/bucket/key?X-Amz-Signature=...
```

client 한 개로는 SDK 호출 (Exists, Stat, Remove) 과 presigning 의 host 를 다르게 둘 수 없음.

### 권장 패턴 — Dual-client

```go
type MinIOBackend struct {
    sdkClient  *minio.Client  // cluster-internal svc 로 SDK 호출 (low latency)
    signClient *minio.Client  // external host 로 presigning (client reachable)
    bucket     string
    ...
}

func NewMinIOBackend(internalEndpoint, externalBaseURL, ak, sk, bucket, region string, ...) (*MinIOBackend, error) {
    sdkClient, err := minio.New(internalEndpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(ak, sk, ""),
        Secure: false,  // cluster-internal HTTP
        Region: region,
    })
    if err != nil { return nil, err }

    extURL, err := url.Parse(externalBaseURL)
    if err != nil { return nil, err }
    signClient, err := minio.New(extURL.Host, &minio.Options{
        Creds:  credentials.NewStaticV4(ak, sk, ""),
        Secure: extURL.Scheme == "https",
        Region: region,
    })
    if err != nil { return nil, err }

    return &MinIOBackend{sdkClient: sdkClient, signClient: signClient, bucket: bucket}, nil
}

// 메서드 매핑
func (b *MinIOBackend) Exists(...)         → b.sdkClient.StatObject(...)
func (b *MinIOBackend) Delete(...)         → b.sdkClient.RemoveObject(...)
func (b *MinIOBackend) GenerateUploadURL   → b.signClient.PresignedPutObject(...)
func (b *MinIOBackend) GenerateDownloadURL → b.signClient.PresignedGetObject(...)
```

**왜 두 client?**
- `sdkClient` (cluster-internal): pod ↔ MinIO 직접 통신. istio gateway 우회 → latency 낮음, 외부 망 의존 0.
- `signClient` (external): presigned URL 의 host 가 client (git-lfs CLI on user laptop) 도달 가능해야 함. 외부 도메인 으로 sign 해야 함.

### 대안 검토 (기각)

| 옵션 | 이유 |
|---|---|
| Single client 로 external endpoint 만 사용 | git-executor pod 의 모든 SDK 호출이 istio gateway 경유 → 불필요한 latency, gateway 부담 |
| Single client 로 internal + presigned URL host rewrite 후처리 | minio-go 의 signature 가 host 포함 → host 변경 시 signature invalid |
| presigned URL 미사용, 모든 byte 가 git-executor proxy 통과 | 5GB+ multipart 시 git-executor pod 메모리 / 네트워크 폭발. 현 local backend 패턴. 확장성 X |

### Phase 1.2 영향
- Issue-2357 (MinIO backend) 구현 시 dual-client 패턴 명시적으로 적용.
- 단위 테스트 (Issue-2361 의 testcontainers) 에서 두 endpoint 분리 검증.

### 후속 액션
- [ ] Issue-2357 본문에 dual-client 패턴 명시 (이미 설계 문서에 있지만 이슈 본문 강조)

---

## 결론 — Phase 1 진행 가능 여부

| 항목 | 결과 | Phase 1 영향 |
|---|---|---|
| (1) obj 도달성 | ✅ OK (stage / onprem-stage 가능), onprem-skt 별도 확인 | helm values 환경별 `LFS_BASE_URL` 다르게 |
| (2) GCS force-disable | ✅ 의도된 onprem-only override | Issue-2358 스코프 축소 — 제거가 아니라 주석 보강 + minio case 추가 |
| (3) Presigned URL host | ⚠️ dual-client 필수 (설계 시 이미 반영됨) | Issue-2357 구현 시 dual-client 명시 |

**Phase 1 진행 OK** — blocker 없음. Issue-2356 (objstore PRIVATE_BUCKETS) + Issue-2357 (MinIO backend) 병렬 착수 가능.

### 사이드 발견 (별도 트래킹 권장)

1. **onprem-skt 의 외부 도메인 확인** — Phase 4 rollout 전 spike 필요
2. **순수 onprem (off-domain) 의 LFS 클라이언트 접근 정책** — VPN / port-forward / 클러스터 내부에서만 LFS 사용? — Phase 4 결정 사항
3. **`docs/handoff-fork-api.md:235` 의 "LFS 객체는 GCS에 있다" 가정 obsolete** — MinIO 로 전환되므로 해당 부채 description 업데이트 필요 (Phase 4 docs 작업과 함께)
