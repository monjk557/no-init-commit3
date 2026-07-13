#!/usr/bin/env bash
#
# check-openapi-refs.sh — Issue-5806 guard.
#
# Fails CI if the committed OpenAPI spec references a component schema under
# #/components/schemas/ that is never defined in components.schemas — a dangling
# $ref. Such a ref parses as valid YAML but breaks downstream OpenAPI codegen
# (the orchestrator's DTO generation), so it must be caught here, in git-executor
# CI, instead of after the broken spec ships.
#
# Root cause is usually a new typed domain whose schema.go was omitted from
# schemaFilePaths() in specs/generate.go: the route registers and emits a $ref,
# but the schema type is never parsed into the spec.
#
# The generator self-validates too (validateSchemaRefs in specs/generate.go), so
# `make specs` already fails on a fresh dangling ref. This guard is the committed
# -artifact backstop: it flags a broken spec that was committed regardless of how.

set -uo pipefail

SPEC="${SPEC:-specs/generated/collabops-git-executor.yaml}"

if [ ! -f "$SPEC" ]; then
	echo "✗ [openapi-ref-guard] spec not found: $SPEC" >&2
	exit 1
fi

echo "▶ [openapi-ref-guard] checking $SPEC for dangling schema \$refs"

# Referenced schema names: every #/components/schemas/NAME occurrence.
refs=$(grep -oE '#/components/schemas/[A-Za-z0-9_]+' "$SPEC" \
	| sed 's#.*/##' | sort -u)

# Defined schema names: 4-space-indented keys inside the `  schemas:` block,
# which ends at the next 2-space sibling section (e.g. `  responses:`).
defined=$(awk '
	/^  schemas:[[:space:]]*$/ { inblock=1; next }
	inblock && /^  [^ ]/       { inblock=0 }
	inblock && /^    [A-Za-z0-9_]+:/ {
		name=$1; sub(/:.*/, "", name); print name
	}
' "$SPEC" | sort -u)

missing=$(comm -23 <(printf '%s\n' "$refs") <(printf '%s\n' "$defined"))

if [ -n "$missing" ]; then
	echo "✗ [openapi-ref-guard] dangling \$ref — referenced but not defined in components.schemas:" >&2
	printf '    - %s\n' $missing >&2
	echo "" >&2
	echo "  → add the owning domain's schema.go to schemaFilePaths() in specs/generate.go, then: make specs" >&2
	exit 1
fi

echo "✓ [openapi-ref-guard] passed — every schema \$ref resolves."
