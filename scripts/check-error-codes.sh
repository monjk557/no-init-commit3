#!/usr/bin/env bash
#
# check-error-codes.sh — Issue-5577 ratchet guard.
#
# Fails CI if handler code reintroduces the pre-5577 error pattern:
#   1) bare `typed.NewHTTPError(status, msg)` — an error response with NO stable
#      machine error_code. New handlers must route through the central catalog
#      (`api.NewCoded(api.ErrCodeX, ...)`), or `typed.NewHTTPErrorWithCode(...)`
#      for packages that sit below `api` in the import graph (tree, repositories).
#   2) HTTP 422 (`StatusUnprocessableEntity`) emitted from production code — 422
#      mislabels server-side state as a client error. Route by cause instead:
#      missing repo → 404, empty repo → 200, corrupted/unexpected → 500.
#
# The Go test TestCatalogHasNo422 guards the catalog itself; this script guards
# every handler at the source level so a raw call can't slip past the catalog.
#
# Scope: production Go under internal/ and cmd/. Excludes:
#   - *_test.go            (tests may assert legacy behaviour / use fixtures)
#   - internal/ruleguard/client.go (interprets an upstream 422, does not emit it)

set -uo pipefail

roots="internal cmd"
fail=0

echo "▶ [errcode-guard] 1/2 — bare typed.NewHTTPError (missing error_code)"
bare=$(grep -rn --include='*.go' 'typed\.NewHTTPError(' $roots 2>/dev/null \
	| grep -v '_test\.go:' \
	| grep -v 'NewHTTPErrorWithCode' || true)
if [ -n "$bare" ]; then
	echo "  ✗ found bare typed.NewHTTPError — use api.NewCoded(api.ErrCodeX, msg)"
	echo "$bare" | sed 's/^/      /'
	fail=1
else
	echo "  ✓ none"
fi

echo "▶ [errcode-guard] 2/2 — HTTP 422 in production code"
u422=$(grep -rn --include='*.go' 'StatusUnprocessableEntity' $roots 2>/dev/null \
	| grep -v '_test\.go:' \
	| grep -v 'internal/ruleguard/client\.go:' || true)
if [ -n "$u422" ]; then
	echo "  ✗ found HTTP 422 — forbidden (missing→404, empty→200, corrupted→500)"
	echo "$u422" | sed 's/^/      /'
	fail=1
else
	echo "  ✓ none"
fi

if [ "$fail" -ne 0 ]; then
	echo ""
	echo "✗ [errcode-guard] FAILED (Issue-5577). Route errors through the central"
	echo "  catalog: api.NewCoded(api.ErrCodeX, msg) — see internal/api/errcatalog.go."
	exit 1
fi

echo "✓ [errcode-guard] passed — all errors carry a code, no 422."
