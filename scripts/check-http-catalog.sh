#!/usr/bin/env bash
# guard-http-catalog — executor 도메인 가드: http substrate 응답을 에러 카탈로그로 만든다.
#
# HTTP-PROTOCOL §1/§2 (harness docs) + GO-STANDARD §6: 응답은 계약이다. 핸들러가 ad-hoc
# `gin.H{}` 로 응답을 손으로 짜면 엔벨로프가 엔드포인트마다 갈라진다. 표준 경로는
# api.NewCoded(카탈로그) + typed wrapper. 이행 중이라 래칫으로 신규 유입만 차단한다.
#
# ratchet: baseline 은 현재 잔존 gin.H 수. 초과 시 fail, 감소 시 baseline 하향 권장.
set -eu

BASE_GINH=77
cur=$(grep -rn 'gin\.H{' --include='*.go' internal/ 2>/dev/null | grep -v _test | wc -l | tr -d ' ')
if [ "$cur" -gt "$BASE_GINH" ]; then
  echo "✗ gin.H 응답 $BASE_GINH → $cur (+$((cur - BASE_GINH))): ad-hoc 응답 유입."
  echo "  에러는 api.NewCoded(카탈로그), 성공은 typed wrapper 로 — 엔벨로프 일관성(HTTP-PROTOCOL)."
  exit 1
fi
echo "✓ gin.H 응답 $cur ≤ $BASE_GINH (에러 카탈로그로 수렴 중)"
[ "$cur" -lt "$BASE_GINH" ] && echo "  개선: BASE_GINH 을 $cur 로 낮추세요."
echo "✅ guard-http-catalog 통과"
exit 0
