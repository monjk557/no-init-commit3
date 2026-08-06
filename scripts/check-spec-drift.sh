#!/usr/bin/env bash
# guard-spec-drift — 핸들러 시그니처가 OpenAPI 스펙의 SoT(HTTP-PROTOCOL §3, GO-STANDARD §6).
#
# 스펙을 핸들러에서 재생성(make specs)해 커밋본과 diff — 다르면 핸들러/스키마를 바꾸고
# 스펙 재생성·커밋을 안 한 것(드리프트). downstream(orchestrator DTO codegen)이 낡은
# 스펙을 쓰게 두지 않는다. dangling $ref(guard-specs)와 상보적: 이건 "핸들러↔스펙 동기".
set -eu

SPEC=specs/generated/collabops-git-executor.yaml

echo "🔁 핸들러에서 스펙 재생성 (make specs)..."
if ! make specs >/tmp/spec-drift-gen.log 2>&1; then
  echo "✗ 스펙 재생성 실패 (make specs):"
  tail -5 /tmp/spec-drift-gen.log | sed 's/^/    /'
  exit 1
fi

if ! git diff --quiet -- "$SPEC" 2>/dev/null; then
  echo "✗ OpenAPI 스펙 드리프트 — 핸들러/스키마 변경 후 스펙 재생성·커밋 누락:"
  git --no-pager diff --stat -- "$SPEC" | sed 's/^/    /'
  echo "  조치: 로컬에서 'make specs' 실행 후 $SPEC 를 커밋하세요."
  exit 1
fi

echo "✓ 스펙 드리프트 없음 — 핸들러 = 스펙 SoT 일치."
echo "✅ guard-spec-drift 통과"
exit 0
