# ENV Deletion Investigation: CR Squash-Merge Critical Issue

> **Priority**: P0 - Production Critical
> **Status**: In Investigation
> **Impact**: Data loss in staging environments
> **Date Started**: 2026-02-24

## Problem Statement

**Core Issue**: During Code Review (CR) squash-and-merge operations, all files under `config/env/` are completely deleted despite:
- 14 files changed (no env modifications in the changeset)
- No previous env-related deletions in the branch history
- Clean merge conditions (no manual conflicts expected)

**Business Impact**:
- Staging environment configuration loss
- Service downtime during deployment
- Manual recovery required for each incident
- Developer productivity loss

---

## Investigation Structure

### Phase 1: Root Cause Analysis ✅ COMPLETED
- [Initial Analysis](./env-deletion-analysis.md) - Identified 5 critical bugs in merge logic
- [Impact Measurement & Test Results](./logs.md) - Current baseline and success tracking
- **Key Finding**: Recursive merge strategy always prioritizes SOURCE over TARGET in conflicts

### Phase 2: Platform Benchmarking 🔄 IN PROGRESS
- [GitLab Behavior Analysis](./benchmarking/gitlab-analysis.md)
- [GitHub Behavior Analysis](./benchmarking/github-analysis.md)
- [Gitea Behavior Analysis](./benchmarking/gitea-analysis.md)
- [Industry Standards Research](./benchmarking/standards-research.md)

### Phase 3: Solution Architecture 📋 PLANNED
- [Long-term Architectural Design](./solutions/architecture.md)
- [Multi-phase Implementation Plan](./solutions/implementation-plan.md)
- [Risk Mitigation Strategy](./solutions/risk-mitigation.md)

### Phase 4: Implementation & Testing 📋 PLANNED
- Conflict Detection Algorithm Redesign
- Merge Strategy Intelligence Enhancement
- Comprehensive Test Suite
- Production Rollout Plan

---

## Critical Bugs Identified

### 🚨 Bug #1: Conflict Over-Detection (Severity: HIGH)
**File**: `internal/merge/conflict_detector.go:284`
**Issue**: Normal single-side changes incorrectly classified as conflicts
**Impact**: Forces users to use `force_conflict=true`, enabling other bugs

### 🚨 Bug #2: SOURCE Priority in Recursive Strategy (Severity: CRITICAL)
**File**: `internal/merge/apply_tree.go:202-214`
**Issue**: Always chooses feature branch over staging in conflicts
**Impact**: Direct cause of env file deletion

### ⚠️ Bug #3: Delete Classification Missing
**File**: `internal/merge/conflict_detector.go:364-407`
**Issue**: "Delete vs unchanged" incorrectly classified as content conflict
**Impact**: Wrong severity assessment, incorrect auto-resolution

### ⚠️ Bug #4: Squash Merge Tree Replacement
**File**: `internal/merge/strategy_merge.go:134-138`
**Issue**: Returns entire source tree when conflicts + !force_conflict
**Impact**: Potential for complete target environment override

### 🔧 Bug #5: Dead Code in File Resolution
**File**: `internal/merge/apply_tree.go:217-238`
**Issue**: Case 7,8 unreachable due to earlier identical conditions
**Impact**: Logic confusion, maintenance burden

---

## Immediate Actions Required

### 1. Production Safeguards
- [ ] Emergency config backup automation
- [ ] Pre-merge env validation hooks
- [ ] Rollback automation for env loss incidents

### 2. Investigation Continuation
- [ ] Platform behavior benchmarking
- [ ] Industry standards research
- [ ] User impact assessment surveys

### 3. Solution Development
- [ ] Intelligent merge strategy design
- [ ] Adaptive conflict resolution system
- [ ] Self-healing environment management

---

## Success Metrics

**Short-term (1 month)**:
- Zero env deletion incidents
- 95%+ clean merge success rate without force_conflict
- <30s average merge processing time

**Long-term (6 months)**:
- Intelligent conflict resolution (90% auto-resolvable)
- Predictive merge outcome modeling
- Self-healing environment coordination
- 99.9% configuration preservation rate

---

**Next Steps**: Begin [Platform Benchmarking Phase](./benchmarking/) to understand industry best practices and design intelligent merge algorithms.