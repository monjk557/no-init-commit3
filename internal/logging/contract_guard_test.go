// Guard — 로그 라인 계약 (Issue-8015).
//
// 이 레포가 **실제로 내보내는 로그 줄**이 계약을 지키는지 검증한다.
// 계약 본문은 .collabops/log.schema.json — collabops-code-harness 사본이며,
// harness log-guard 가 원본과 sha256 이 같은지 대조한다(임의로 고칠 수 없다).
//
// 검증 대상은 인코더의 **출력 shape** 이다. call site 를 grep 해서는 안 보이는
// 결함 — zap 기본 인코더가 ts·caller 를 top-level 에 싣고 level 을 warn 으로
// 내보내던 것 — 을 잡기 위해 실제 emit 된 줄을 본다.
//
// 로컬 재현:  make guard-logshape
package logging_test

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const probeSecret = "probe-secret-do-not-log"

// 계약상 metadata 안에 있으면 안 되는 이름들 (top-level 로 올라가야 한다).
var reservedNames = []string{
	"event", "level", "msg", "error_code", "status_code", "request_id",
	"trace_id", "span_id", "method", "path", "duration_ms",
	"request_size_bytes", "request_content_type", "request_body_keys", "exc_info",
}

func emitted(t *testing.T) []map[string]any {
	t.Helper()
	// 테스트는 패키지 디렉터리에서 돈다. 프로브 경로가 레포 루트 기준이라 cwd 를 옮긴다.
	cmd := exec.Command("go", "run", "./.collabops/log-probe")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("프로브 실행 실패: %v\n%s", err, stderr)
	}

	var docs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("프로브 출력이 JSON 이 아니다: %v\n%s", err, line)
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		t.Fatal("프로브가 아무것도 내보내지 않았다")
	}
	return docs
}

func compiledSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", ".collabops", "log.schema.json")
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(path)
	if err != nil {
		t.Fatalf("계약 스키마 컴파일 실패: %v", err)
	}
	return sch
}

func TestAllLogLinesSatisfyContract(t *testing.T) {
	sch := compiledSchema(t)
	for i, doc := range emitted(t) {
		if err := sch.Validate(doc); err != nil {
			t.Errorf("%d줄 계약 위반: %v", i+1, err)
		}
	}
}

// 2xx / 4xx / 5xx / 비-HTTP 를 각 1줄 이상.
// 이게 없으면 통과만 하는 빈껍데기 프로브로 가드를 무력화할 수 있다.
func TestProbeCoversRepresentativeCases(t *testing.T) {
	var has2xx, has4xx, has5xx, hasNonHTTP bool
	for _, doc := range emitted(t) {
		raw, ok := doc["status_code"]
		if !ok {
			hasNonHTTP = true
			continue
		}
		code := int(raw.(float64))
		switch {
		case code >= 500:
			has5xx = true
		case code >= 400:
			has4xx = true
		case code >= 200 && code < 300:
			has2xx = true
		}
	}
	if !has2xx || !has4xx || !has5xx || !hasNonHTTP {
		t.Errorf("프로브 커버리지 부족: 2xx=%v 4xx=%v 5xx=%v 비-HTTP=%v",
			has2xx, has4xx, has5xx, hasNonHTTP)
	}
}

// status_code 가 있으면 level 이 계약 매핑을 따라야 한다 (§5).
func TestLevelFollowsStatus(t *testing.T) {
	for _, doc := range emitted(t) {
		raw, ok := doc["status_code"]
		if !ok {
			continue
		}
		code := int(raw.(float64))
		want := "info"
		switch {
		case code >= 500:
			want = "error"
		case code >= 400:
			want = "warning"
		}
		if doc["level"] != want {
			t.Errorf("status_code=%d 인데 level=%v (기대 %s)", code, doc["level"], want)
		}
	}
}

// 계약 enum 은 warning 이다. zap 기본은 warn 이라 그대로 두면 전 서비스 단일
// 쿼리에서 이 서비스만 빠진다.
func TestLevelUsesContractEnum(t *testing.T) {
	sawWarning := false
	for _, doc := range emitted(t) {
		if doc["level"] == "warn" {
			t.Error("level 이 warn 이다 — 계약 enum 은 warning")
		}
		if doc["level"] == "warning" {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Error("warning 줄이 없다 — 이 검사가 무의미해진다")
	}
}

func TestErrorLinesCarryErrorCode(t *testing.T) {
	for _, doc := range emitted(t) {
		raw, ok := doc["status_code"]
		if !ok {
			continue
		}
		if int(raw.(float64)) >= 400 && doc["error_code"] == nil {
			t.Errorf("status_code=%v 인데 error_code 가 없다 — 로그를 봐도 무슨 에러인지 알 수 없다", raw)
		}
	}
}

func TestReservedNamesNotHiddenInMetadata(t *testing.T) {
	for i, doc := range emitted(t) {
		meta, ok := doc["metadata"].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range reservedNames {
			if _, hidden := meta[name]; hidden {
				t.Errorf("%d줄 metadata 에 top-level 예약어가 있다: %s", i+1, name)
			}
		}
	}
}

// Issue-8093 — 마스킹이 뚫리면 평문이 나온다.
func TestNoPlaintextSecretLeaks(t *testing.T) {
	for i, doc := range emitted(t) {
		raw, _ := json.Marshal(doc)
		if strings.Contains(string(raw), probeSecret) {
			t.Errorf("%d줄에 민감정보가 평문으로 나갔다 — 인코더의 마스킹이 뚫렸다", i+1)
		}
	}
}

// 마스킹이 "계측 필드까지 지우는 것" 으로 퇴화하지 않았는지.
// app-web 에서 jwt_ms 가 jwt 에 걸려 전량 [REDACTED] 된 실측 사례가 있다.
func TestMaskingKeepsMeasurementFields(t *testing.T) {
	found := false
	for _, doc := range emitted(t) {
		meta, ok := doc["metadata"].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := meta["jwt_ms"]; ok {
			found = true
			if v == "[REDACTED]" {
				t.Error("jwt_ms 가 마스킹됐다 — 소요시간은 자격증명이 아니다")
			}
		}
	}
	if !found {
		t.Error("jwt_ms 샘플이 없다 — 이 검사가 무의미해진다")
	}
}
