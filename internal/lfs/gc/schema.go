// Package gc 는 LFS object 의 garbage collection 용 reachability scan endpoint
// 를 제공한다.
//
// orchestrator 의 GC cron 이 호출하여 git ref 에서 도달 가능한 LFS pointer
// OID 의 set 을 얻고, 그것과 MinIO 의 object 목록을 차집합하여 orphan 을
// 식별 / 삭제한다.
//
// 본 endpoint 는 read-only — git object DB 만 읽고 어떤 byte 도 변경하지
// 않는다. MinIO 에는 접근하지 않는다 (그건 orchestrator 의 책임).
package gc

// ScanReachableOIDsResponse 는 git ref 에서 도달 가능한 LFS pointer OID 의 응답.
//
// 응답 크기 추정: 평균 64 hex char + JSON overhead ≈ 80 byte/oid. 1000개 OID 일 때 ~80KB.
// 대형 monorepo (10만개 OID) 도 ~8MB — orchestrator 의 cron 컨텍스트 라 OK.
type ScanReachableOIDsResponse struct {
	// Success 는 항상 true (실패는 typed.HTTPError 로 status 매핑).
	Success bool `json:"success"`

	// ReachableOIDs 는 git ref (branches + tags) 에서 도달 가능한 LFS pointer
	// 의 OID 목록 (lowercase hex, 64 chars). 중복 제거되어 있다.
	ReachableOIDs []string `json:"reachable_oids"`

	// ScannedRefs 는 scan 대상이 된 ref 수 (branches + tags).
	ScannedRefs int `json:"scanned_refs"`

	// ScannedBlobs 는 검사한 blob 수 (LFS pointer 인지 / 아닌지 무관).
	ScannedBlobs int `json:"scanned_blobs"`

	// LFSPointerBlobs 는 LFS pointer 로 식별된 blob 수.
	// 같은 OID 가 여러 ref / path 에서 등장할 수 있으므로 ReachableOIDs 보다
	// 크거나 같다.
	LFSPointerBlobs int `json:"lfs_pointer_blobs"`

	// Truncated 는 max_oids cap 에 도달해서 일부 OID 가 빠졌는지.
	// true 면 orchestrator 가 partial 결과로 처리해야 함 — 다음 scan 까지
	// GC 를 보류하는 게 안전.
	Truncated bool `json:"truncated"`

	// DurationMs 는 scan 소요 시간 (ms). observability 용.
	DurationMs int64 `json:"duration_ms"`
}
