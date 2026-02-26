# Deep Dive Analysis: Git Merge Strategies and File Deletion Behavior

## Executive Summary

This comprehensive analysis examines Git's merge strategies with a focus on file deletion scenarios, conflict resolution algorithms, and the specific behaviors that can lead to selective file deletion during merge operations. The research combines examination of Git's official documentation, source code analysis, and real-world implementation patterns observed in the CollabOps Git Executor codebase.

## 1. Git Merge Strategy Overview

### 1.1 Available Merge Strategies

Git provides six primary merge strategies, each with distinct characteristics and use cases:

| Strategy | Type | Heads Supported | Primary Use Case | File Deletion Behavior |
|----------|------|----------------|------------------|----------------------|
| **ort** | 3-way | 2 | Default strategy (v2.33.0+) | Complex deletion decision tree |
| **recursive** | 3-way | 2 | Legacy default (pre-v2.33.0) | Similar to ort but slower |
| **resolve** | 3-way | 2 | Conservative merging | Simple 3-way deletion rules |
| **octopus** | N-way | 2+ | Multiple branch merging | Refuses complex deletions |
| **ours** | Override | Any | Keep current branch entirely | No deletions from other branches |
| **subtree** | 3-way | 2 | Subtree project merging | Tree-adjusted deletion logic |

### 1.2 Default Strategy Evolution

```
Git v0.99.9k - v2.33.0: recursive (default)
Git v2.33.0+:           ort (default)
```

The **ORT** (Ostensibly Recursive's Twin) strategy replaced recursive as the default due to:
- Significant performance improvements (orders of magnitude faster in some scenarios)
- Better handling of rename detection
- More sophisticated conflict resolution algorithms
- Cleaner memory usage patterns

## 2. ORT Strategy Deep Dive: File Deletion Logic

### 2.1 Core Deletion Detection Algorithm

The ORT strategy implements a sophisticated decision tree for file deletion scenarios through several key functions:

#### `collect_rename_info()` - Deletion Identification
```c
// Pseudocode from Git source analysis
if (filemask & 1) && !(filemask & side_mask) {
    // File exists in base but missing on this side
    mark_as_deletion_candidate(file, side);
}
```

This function examines filemask patterns to identify when files present in the merge base disappear on one or both sides of the merge.

#### Decision Tree for File Preservation vs. Deletion

The ORT algorithm uses a multi-criteria evaluation system:

**1. Content Relevance Assessment**
- **Content Relevant**: `match_mask == 0` (file changed across branches)
- **Location Relevant**: `dir_rename_mask == 0x07` (directory rename involved)

**2. Match Mask Evaluation**
```c
if (sides_match && filemask == 0x07) {
    // All three paths present and files match - preserve unchanged
    preserve_file(file);
}
```

**3. Staged Conflict Resolution**
The strategy processes deletions in three stages:
- **Stage 1**: Common ancestor version
- **Stage 2**: HEAD (current branch) version
- **Stage 3**: MERGE_HEAD (incoming branch) version

### 2.2 Critical File Deletion Scenarios

#### Scenario 1: File Missing in Source, Unchanged in Target
```
Base:   file.txt (content: "original")
Source: [file.txt deleted]
Target: file.txt (content: "original")
```

**ORT Strategy Decision Tree:**
```
1. Check if file unchanged between base and target
   └─ YES: file.baseHash == file.targetHash
2. Check merge strategy
   └─ theirs: Respect deletion (return ZeroHash)
   └─ ours/recursive/ort:
       ├─ Check protection context
       └─ Default: Preserve file (return targetHash)
```

#### Scenario 2: File Modified in Target, Deleted in Source
```
Base:   file.txt (content: "original")
Source: [file.txt deleted]
Target: file.txt (content: "modified")
```

**ORT Strategy Decision Tree:**
```
1. Detect modification: file.baseHash != file.targetHash
2. Conflict: Modified vs Deleted
3. Strategy resolution:
   └─ theirs: Keep deletion (ZeroHash)
   └─ ours: Keep modification (targetHash)
   └─ recursive/ort: Default to preservation (targetHash)
```

#### Scenario 3: File Deleted in Both Branches
```
Base:   file.txt (content: "original")
Source: [file.txt deleted]
Target: [file.txt deleted]
```

**Result:** File definitively deleted (ZeroHash) - no conflict

### 2.3 Directory vs File Type Conflicts

The ORT strategy includes sophisticated handling for type conflicts where directories and files have the same path:

```c
// Type conflict resolution priority
if (sourceIsDir && targetIsFile) {
    // Prefer directory over file (default strategy)
    return merge_directory_entry();
} else if (strategy == MergeStrategyTheirs) {
    return sourceEntry; // Respect source decision
}
```

## 3. Merge Strategy Comparison: File Deletion Behavior

### 3.1 Strategy-Specific Deletion Rules

#### **ort/recursive Strategies**
- **Philosophy**: Preserve content when possible, minimize data loss
- **Deletion Criteria**:
  - File unchanged between base and target → preserve despite source deletion
  - File modified in both branches → prefer modification over deletion
  - Environment files → require explicit confirmation for deletion
- **Performance**: ort is significantly faster due to tree-based processing

#### **resolve Strategy**
- **Philosophy**: Conservative 3-way merge with minimal assumptions
- **Deletion Criteria**:
  - Strict 3-way comparison: Base vs Source vs Target
  - No special handling for directory structure differences
  - Does not handle renames (unlike ort/recursive)
- **Conflict Detection**: Carefully detects criss-cross merge ambiguities

#### **ours Strategy**
- **Philosophy**: Current branch supremacy
- **Deletion Behavior**:
  - Never deletes files present in current branch
  - Ignores all changes from other branches entirely
  - Result tree = always current branch tree
- **Use Case**: Release branch protection, rollback scenarios

#### **theirs Strategy** (via -X theirs)
- **Philosophy**: Incoming branch supremacy
- **Deletion Behavior**:
  - Respects all deletions from incoming branch
  - Overwrites current branch modifications
  - Useful for accepting upstream changes wholesale

### 3.2 Configuration Impact on Deletion Behavior

#### Core Configuration Options
```bash
# Rename detection affects deletion interpretation
git config merge.renames true
git config merge.renameLimit 1000

# Directory rename handling
git config merge.directoryRenames true

# CRLF normalization can affect file change detection
git config merge.renormalize true

# Conflict marker style affects manual resolution
git config merge.conflictStyle diff3
```

#### Strategy-Specific Options
```bash
# ORT strategy options that affect deletion
git merge -X ours           # Favor current branch in conflicts
git merge -X theirs         # Favor incoming branch in conflicts
git merge -X find-renames   # Enable rename detection
git merge -X no-renames     # Disable rename detection
git merge -X ignore-space-change # Ignore whitespace for conflict detection
```

## 4. Custom Merge Drivers and File Preservation

### 4.1 Custom Driver Configuration

For specialized file handling (e.g., environment files, configuration):

```bash
# Define custom merge driver
git config merge.preserve-env.name "Environment File Preservation Driver"
git config merge.preserve-env.driver "/path/to/preserve-script %O %A %B %L"
git config merge.preserve-env.recursive "preserve-env"
```

**Example .gitattributes:**
```
config/env/* merge=preserve-env
*.env merge=preserve-env
environments/* merge=preserve-env
```

### 4.2 Driver Implementation Pattern

```bash
#!/bin/bash
# preserve-script implementation
BASE="$1"    # %O - common ancestor
CURRENT="$2" # %A - current branch (HEAD)
OTHER="$3"   # %B - other branch (MERGE_HEAD)
MARKER="$4"  # %L - conflict marker size

# Preserve current branch for environment files
cp "$CURRENT" "$CURRENT.tmp"
exit 0  # Signal clean merge
```

## 5. Analysis of CollabOps Implementation

### 5.1 Current Merge Strategy Implementation

The CollabOps Git Executor implements a sophisticated protection system that wraps around Git's merge strategies:

**Key Components:**
- `MergeProtectionContext` - Feature flag controlled file preservation
- Environment file detection and protection
- Fallback mechanisms for failed merges
- Comprehensive logging for merge decisions

**Critical Implementation Detail:**
```go
// From strategy_merge.go line 166-173
if protectionCtx != nil && protectionCtx.ShouldPreserveFile(entry.path) {
    h.logger.Info("Protection system preserving file from deletion",
        zap.String("path", entry.path),
        zap.String("protection_mode", protectionCtx.GetProtectionStatus()))
    return entry.targetHash, entry.targetMode, nil
}
```

### 5.2 Identified Risk Patterns

1. **Environment File Exposure**: Without protection context, environment files can be deleted during merges
2. **Directory Structure Differences**: Missing directories in source can lead to file deletion
3. **Rename Detection Gaps**: Files moved in source but modified in target may be lost
4. **Fallback Strategy Risks**: Failed merge tree creation defaults to potentially unsafe fallbacks

## 6. Performance and Correctness Trade-offs

### 6.1 Strategy Performance Characteristics

| Strategy | Performance | Rename Detection | Complex Conflicts | Memory Usage |
|----------|-------------|------------------|------------------|--------------|
| **ort** | Excellent | Full | Advanced | Low |
| **recursive** | Good | Full | Advanced | Medium |
| **resolve** | Fast | None | Basic | Low |
| **octopus** | Variable | Limited | Refuses | High |
| **ours** | Fastest | N/A | None | Minimal |
| **subtree** | Good | Full | Advanced | Medium |

### 6.2 Correctness Considerations

#### Known Algorithm Limitations

From Git documentation, all 3-way merge strategies share a fundamental limitation:

> "If a change is made on both branches, but later reverted on one of the branches, that change will be present in the merged result. This occurs because only the heads and the merge base are considered when performing a merge, not the individual commits."

This can lead to unexpected file resurrections or deletions when:
- Changes are made and then reverted in one branch
- The revert is interpreted as "no change" relative to the base
- The algorithm substitutes the changed version from the other branch

## 7. Recommendations for Long-Term Architectural Solutions

### 7.1 Intelligent Merge Strategy Selection

**Adaptive Strategy Engine:**
```go
type AdaptiveMergeStrategy struct {
    repo                *Repository
    conflictAnalyzer    *ConflictAnalyzer
    protectionPolicies  []ProtectionPolicy
    performanceMonitor  *PerformanceMonitor
}

func (ams *AdaptiveMergeStrategy) SelectOptimalStrategy(
    ctx context.Context,
    merge *MergeRequest,
) (MergeStrategy, error) {
    // Analyze repository characteristics
    repoMetrics := ams.analyzeRepository(merge.SourceRef, merge.TargetRef)

    // Evaluate conflict potential
    conflictRisk := ams.conflictAnalyzer.AssessRisk(merge)

    // Consider protection requirements
    protectionLevel := ams.evaluateProtectionNeeds(merge)

    // Select strategy based on multi-dimensional optimization
    return ams.optimizeStrategy(repoMetrics, conflictRisk, protectionLevel)
}
```

### 7.2 Predictive Conflict Resolution System

**Multi-Phase Conflict Prediction:**
```go
type PredictiveConflictResolver struct {
    historyAnalyzer     *HistoryAnalyzer
    semanticAnalyzer    *SemanticAnalyzer
    machineLearnModel   *ConflictPredictionModel
}

// Predict conflicts before they occur
func (pcr *PredictiveConflictResolver) PredictAndPrevent(
    merge *MergeRequest,
) (*ConflictPrevention, error) {
    // Phase 1: Historical pattern analysis
    historicalConflicts := pcr.historyAnalyzer.AnalyzeConflictPatterns(
        merge.SourceRef, merge.TargetRef)

    // Phase 2: Semantic code analysis
    semanticConflicts := pcr.semanticAnalyzer.DetectSemanticConflicts(
        merge.SourceRef, merge.TargetRef)

    // Phase 3: ML-based prediction
    predictedConflicts := pcr.machineLearnModel.PredictConflicts(
        historicalConflicts, semanticConflicts)

    // Generate prevention strategies
    return pcr.generatePreventionStrategy(predictedConflicts)
}
```

### 7.3 Self-Healing Merge Infrastructure

**Adaptive Recovery System:**
```go
type SelfHealingMergeSystem struct {
    backupManager      *MergeBackupManager
    recoveryOrchestrator *RecoveryOrchestrator
    learningEngine     *MergeLearningEngine
}

func (shms *SelfHealingMergeSystem) ExecuteResilientMerge(
    ctx context.Context,
    merge *MergeRequest,
) (*MergeResult, error) {
    // Create comprehensive backup
    backup := shms.backupManager.CreateMergeBackup(merge)
    defer backup.Cleanup()

    // Attempt merge with recovery monitoring
    result, err := shms.executeWithRecovery(ctx, merge, backup)
    if err != nil {
        return shms.recoveryOrchestrator.RecoverFromFailure(ctx, merge, backup, err)
    }

    // Learn from successful merge
    shms.learningEngine.UpdateFromSuccess(merge, result)
    return result, nil
}
```

### 7.4 Universal Merge Observability Platform

**Comprehensive Monitoring Architecture:**
```go
type UniversalMergeObservability struct {
    metricsCollector    *MergeMetricsCollector
    traceAnalyzer      *MergeTraceAnalyzer
    alertManager       *MergeAlertManager
    dashboardManager   *MergeDashboardManager
}

func (umo *UniversalMergeObservability) MonitorMergeOperation(
    ctx context.Context,
    merge *MergeRequest,
) *MergeObservationContext {
    // Real-time merge monitoring
    ctx = umo.metricsCollector.InstrumentMerge(ctx, merge)

    // Distributed tracing
    ctx = umo.traceAnalyzer.StartMergeTrace(ctx, merge)

    // Predictive alerting
    umo.alertManager.SetupPredictiveAlerts(ctx, merge)

    // Live dashboard updates
    umo.dashboardManager.StreamMergeProgress(ctx, merge)

    return &MergeObservationContext{
        Context: ctx,
        Metrics: umo.metricsCollector,
        Traces:  umo.traceAnalyzer,
    }
}
```

## 8. Conclusion

Git's merge strategies, particularly the modern ORT algorithm, implement sophisticated decision trees for handling file deletion scenarios. However, these algorithms operate with inherent limitations that can lead to unexpected file deletions or preservations, especially in complex repository structures with:

- Environment configuration differences
- Directory restructuring
- Rename/move operations
- Type conflicts (directory vs. file)

**Key Insights:**

1. **ORT Strategy Sophistication**: The default ORT strategy includes advanced deletion logic that considers file modification patterns, directory structure, and rename detection.

2. **Configuration Criticality**: Merge behavior is heavily influenced by Git configuration options, custom merge drivers, and strategy-specific parameters.

3. **Protection System Necessity**: Systems like CollabOps require additional protection layers beyond Git's built-in strategies to prevent critical file deletion.

4. **Performance vs. Safety**: There's an inherent trade-off between merge performance and deletion safety, requiring intelligent strategy selection.

The long-term architectural solutions proposed focus on building adaptive, self-healing systems that learn from merge patterns, predict conflicts, and provide comprehensive observability - moving beyond simple file preservation to intelligent merge orchestration that scales with repository complexity and organizational needs.

---

## Sources

- [Git merge strategy options & examples | Atlassian Git Tutorial](https://www.atlassian.com/git/tutorials/using-branches/merge-strategy)
- [Git - merge-strategies Documentation](https://git-scm.com/docs/merge-strategies)
- [Git - git-merge Documentation](https://git-scm.com/docs/git-merge)
- [Merge Strategies in Git | Baeldung on Ops](https://www.baeldung.com/ops/git-merge-strategies)
- [git/merge-ort.c at master · git/git](https://github.com/git/git/blob/master/merge-ort.c)
- [The Magic of 3-Way Merge](https://blog.git-init.com/the-magic-of-3-way-merge/)
- [Git Merge Strategies and Algorithms](https://mattrickard.com/git-merge-strategies-and-algorithms)
- [Resolve modify/delete merge conflicts in Git – 4sysops](https://4sysops.com/archives/resolve-modifydelete-merge-conflicts-in-git/)
- [Towards accurate recommendations of merge conflicts resolution strategies - ScienceDirect](https://www.sciencedirect.com/science/article/abs/pii/S0950584923001878)