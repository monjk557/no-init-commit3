package branches

import (
	"regexp"
	"testing"
)

// validator_extra_coverage_test.go — validateBranchName의 모든 검증 분기 커버.
// 순수 로직(h.branchNamePattern만 사용)이라 핸들러를 직접 구성한다.

func newValidatorHandler() *BranchOperationsHandler {
	return &BranchOperationsHandler{
		branchNamePattern: regexp.MustCompile(`^[^\x00-\x20\x7f~^:?*\[\\]+$`),
	}
}

func TestValidateBranchName_AllBranches(t *testing.T) {
	h := newValidatorHandler()

	valid := []string{"feature/foo", "release-1.2", "fix_bug"}
	for _, n := range valid {
		if err := h.validateBranchName(n); err != nil {
			t.Errorf("validateBranchName(%q) unexpected err: %v", n, err)
		}
	}

	invalid := map[string]string{
		"":                "empty",
		"HEAD":            "reserved",
		"orig_head":       "reserved (case-insensitive)",
		"foo.":            "trailing dot",
		"/foo":            "leading slash",
		"foo/":            "trailing slash",
		"a/b/c":           "multiple slashes",
		"a//b":            "multiple slashes (count>1 caught first)",
		"a..b":            "consecutive dots",
		"a b":             "space",
		"a@{b":            "@{ sequence",
		"@":               "exactly @",
		"feature/.hidden": "component starts with dot",
		"feature.lock":    "component ends with .lock",
		"foo~bar":         "invalid char via pattern",
		"foo:bar":         "invalid char colon",
	}
	for name, why := range invalid {
		if err := h.validateBranchName(name); err == nil {
			t.Errorf("validateBranchName(%q) expected error (%s), got nil", name, why)
		}
	}
}

func TestValidateBranchName_NilPatternSkipsCharCheck(t *testing.T) {
	h := &BranchOperationsHandler{branchNamePattern: nil}
	// pattern이 nil이면 문자 블록리스트 검사는 건너뛰지만, 문자열 규칙은 여전히 적용된다.
	if err := h.validateBranchName("ok-name"); err != nil {
		t.Errorf("nil pattern valid name err: %v", err)
	}
	if err := h.validateBranchName("bad."); err == nil {
		t.Error("nil pattern should still reject trailing dot")
	}
}
