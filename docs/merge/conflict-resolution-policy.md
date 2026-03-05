# Conflict Resolution Policy

## Overview

CollabOps Git Executor follows the **industry-standard conflict resolution policy**: all merge conflicts require manual human resolution. There is no auto-resolve mechanism.

## Core Rule

```
has_conflicts = true  →  can_merge = false  (always)
has_conflicts = false →  can_merge = true   (always)
```

These two fields are **mutually exclusive** — `can_merge` and `has_conflicts` can never both be `true`.

## Design Rationale

### Industry Benchmark

Every major Git hosting service enforces the same policy:

| Service | Conflict → Merge Allowed? | Implementation |
|---------|:---:|----------------|
| **GitHub** | NO | `mergeable_state`: `MERGEABLE` / `CONFLICTING` — mutually exclusive |
| **GitLab** | NO | `merge_status`: `can_be_merged` / `cannot_be_merged`. `has_conflicts=true` → always `cannot_be_merged` |
| **Bitbucket** | NO | `can_merge` boolean. Conflicts → always `false` |
| **Gitea/Forgejo** | NO | `mergeable` boolean. Conflicts → always `false` |
| **Azure DevOps** | NO | `mergeStatus`: `succeeded` / `conflicts` — mutually exclusive |

### Why No Auto-Resolve

1. **Safety**: Automated conflict resolution can silently introduce bugs. A machine-resolved merge that compiles doesn't mean it's logically correct.
2. **Accountability**: Manual resolution ensures a human reviewed and approved the merged content.
3. **Predictability**: The API consumer (frontend) always knows: conflicts → block merge. No ambiguous intermediate states.
4. **Simplicity**: One clear rule is easier to implement, test, and reason about than conditional merge-ability.

## API Response Behavior

### Merge Preview (`GET /merge/preview`)

| Scenario | `can_merge` | `has_conflicts` | `conflict_count` |
|----------|:-----------:|:---------------:|:----------------:|
| Clean merge (no conflicts) | `true` | `false` | `0` |
| Fast-forward merge | `true` | `false` | `0` |
| Conflicts detected | `false` | `true` | `> 0` |

### Merge Dry Run (`POST /merge` with `dry_run=true`)

Same semantics as merge preview. `success` reflects whether the merge can proceed:
- `success=true`: no conflicts, merge would succeed
- `success=false`: conflicts exist, merge blocked

### Merge Execution (`POST /merge`)

If conflicts exist and `force_conflict=false` (default), returns `409 Conflict` with conflict details. The response includes:
- List of conflicted files with severity levels
- Recommended resolution actions (all manual)

If `force_conflict=true`, the merge proceeds despite conflicts (using the configured strategy like `ours`/`theirs`). This is an explicit override, not an auto-resolve.

## Deprecated Fields

The following API fields are retained for backward compatibility but always return zero/false values:

| Field | Location | Value | Note |
|-------|----------|-------|------|
| `auto_resolvable` | `FileConflict` | `false` | All conflicts require manual resolution |
| `auto_resolvable` | `MergeAnalysisResult` | `0` | No conflicts are auto-resolvable |
| `auto_resolve` | `MergeAnalysisRequest` | ignored | Request field accepted but not acted upon |
| `resolution` | `FileConflict` | `null` | No auto-generated resolutions |

These fields will be removed in a future API version.

## Frontend Integration Guide

Frontend should implement a simple decision tree:

```
if (response.has_conflicts) {
    // Block merge button
    // Show conflict list with severity
    // Prompt user to resolve conflicts manually
} else {
    // Enable merge button
    // Show merge preview (files changed, commits, etc.)
}
```

Never check `can_merge` and `has_conflicts` independently — they are guaranteed to be consistent inverses of each other (when conflicts are the only factor).
