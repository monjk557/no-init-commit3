package ruleguard

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
)

func encodeBundle(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

const sampleBundleJSON = `{
  "schema_version": 1,
  "repository_id": 123,
  "default_branch": "main",
  "ref_name_sentinel": "${ref_name}",
  "actor": {"id": "u-1", "repo_role_rank": 2, "workspace_role_rank": 1},
  "rulesets": [
    {"id": 12, "name": "prod", "level": "repository", "enforcement": "active",
     "target_type": "branch", "ref_include": ["~DEFAULT_BRANCH"], "ref_exclude": [],
     "bypassed": false,
     "rules": [
       {"id": 5, "kind": "require_change_request", "fail_policy": "closed", "granted": false,
        "expression_ast": {"all": [
          {"op": "==", "left": "action", "right": "push"},
          {"op": "==", "left": "branch.name", "right": "${ref_name}"}
        ]}}
     ]}
  ]
}`

func TestDecodeBundle_RoundTrip(t *testing.T) {
	b, err := DecodeBundle(encodeBundle(t, sampleBundleJSON))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !b.SchemaSupported() {
		t.Fatalf("schema %d should be supported", b.SchemaVersion)
	}
	if b.RepositoryID != 123 || b.DefaultBranch == nil || *b.DefaultBranch != "main" {
		t.Fatalf("bad header: %+v", b)
	}
	if b.Actor.RepoRoleRank != 2 || b.Actor.WorkspaceRoleRank != 1 {
		t.Fatalf("bad actor ranks: %+v", b.Actor)
	}
	if len(b.Rulesets) != 1 || len(b.Rulesets[0].Rules) != 1 {
		t.Fatalf("expected 1 ruleset with 1 rule, got %+v", b.Rulesets)
	}
	if b.Rulesets[0].Rules[0].FailPolicy != "closed" {
		t.Fatalf("fail_policy not parsed: %+v", b.Rulesets[0].Rules[0])
	}
}

func TestDecodeBundle_EndToEndEval(t *testing.T) {
	b, err := DecodeBundle(encodeBundle(t, sampleBundleJSON))
	if err != nil {
		t.Fatal(err)
	}
	// Pushing the default branch → ~DEFAULT_BRANCH applies + branch.name==main → deny.
	res := EvaluateBundleRef(b, "branch", "main", map[string]any{"action": "push", "branch.name": "main"})
	if res.Allowed {
		t.Fatalf("push to default branch should be denied, got %+v", res)
	}
	// Pushing a feature branch → ~DEFAULT_BRANCH does not apply → allowed.
	res = EvaluateBundleRef(b, "branch", "feature/x", map[string]any{"action": "push", "branch.name": "feature/x"})
	if !res.Allowed {
		t.Fatalf("push to feature branch should be allowed, got %+v", res)
	}
}

func TestDecodeBundle_Errors(t *testing.T) {
	if _, err := DecodeBundle(""); err == nil {
		t.Error("empty payload should error")
	}
	if _, err := DecodeBundle("not-base64!!!"); err == nil {
		t.Error("bad base64 should error")
	}
	if _, err := DecodeBundle(base64.StdEncoding.EncodeToString([]byte("not gzip"))); err == nil {
		t.Error("non-gzip should error")
	}
}

func TestSchemaSupported(t *testing.T) {
	if (&Bundle{SchemaVersion: 999}).SchemaSupported() {
		t.Error("unknown schema must be unsupported")
	}
	if !(&Bundle{SchemaVersion: SupportedBundleSchemaVersion}).SchemaSupported() {
		t.Error("current schema must be supported")
	}
	var nilB *Bundle
	if nilB.SchemaSupported() {
		t.Error("nil bundle must be unsupported")
	}
}
