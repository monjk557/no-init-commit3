# Policy-as-Code Framework Documentation

## 🎯 Overview

Complete documentation for the Policy-as-Code Framework - a GitLab/GitHub-style policy engine for large-scale platforms (1000+ developers).

**Version**: 2.0 (Phase 2 Complete)
**Target**: Enterprise platforms using go-git
**Scale**: Optimized for 1000+ developer teams

## 📚 Documentation Structure

### 🚀 Getting Started
- **[User Guide](user-guide.md)** - For developers using the system
- **[Platform Team Guide](platform-team-guide.md)** - For platform teams managing policies
- **[Cookbook](cookbook.md)** - Real-world policy examples and recipes

### 🔧 Operations
- **[Troubleshooting Guide](troubleshooting.md)** - Common issues and solutions
- **[Migration Guide](#migration-from-legacy)** - Upgrading from hard-coded protection

### 📖 Reference
- **[API Reference](#api-reference)** - REST API documentation
- **[Configuration Schema](#configuration-schema)** - YAML policy syntax
- **[Architecture Overview](#architecture)** - System design and components

## 🏗️ Architecture

### Core Components

```
┌─────────────────────────────────────┐
│           Policy Engine             │
├─────────────────────────────────────┤
│  ┌─────────────┐ ┌─────────────────┐ │
│  │  Templates  │ │   Hierarchical  │ │
│  │  Manager    │ │   Policies      │ │
│  └─────────────┘ └─────────────────┘ │
│  ┌─────────────┐ ┌─────────────────┐ │
│  │ Hot Reload  │ │   Validation    │ │
│  │  Manager    │ │   Framework     │ │
│  └─────────────┘ └─────────────────┘ │
└─────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│        Merge Protection             │
│         (Legacy + New)              │
└─────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│           go-git                    │
│      (Git Operations)               │
└─────────────────────────────────────┘
```

### Integration Points

- **Merge Protection**: Seamless integration with existing merge workflows
- **go-git**: Direct integration with Git operations
- **Feature Flags**: Controlled rollout with `COLLABOPS_POLICY_ENGINE_ENABLED`
- **Backward Compatibility**: 100% compatible with existing hard-coded protection

## 🚀 Quick Start

### 1. Enable Policy Engine

```bash
export COLLABOPS_POLICY_ENGINE_ENABLED=true
```

### 2. Create Basic Policy

```yaml
# /etc/collabops/policies/environment-protection.yml
version: "1.0"
metadata:
  name: "Environment Protection"

policies:
  - name: "env-protection"
    enabled: true
    type: "file_protection"
    rules:
      - name: "env-files"
        patterns: ["*.env", "config/**/*.env"]
        action: "block_deletion"
    enforcement: "strict"
```

### 3. Verify Operation

```bash
# Check status
curl -s localhost:8080/api/policy/status

# Test file protection
curl -s "localhost:8080/api/policy/evaluate?file=.env"
```

## 📊 Features by Phase

### Phase 1 (Complete)
- ✅ Basic PolicyEngine interface
- ✅ File-based policy provider
- ✅ Pattern matching with glob support
- ✅ Integration with merge protection
- ✅ Backward compatibility

### Phase 2 (Complete)
- ✅ Template system with variable substitution
- ✅ Hierarchical policy management
- ✅ Hot reload with file watching
- ✅ Comprehensive validation framework
- ✅ Conflict resolution strategies
- ✅ Advanced PolicyEngine interface

## 🎯 Use Cases

### Environment File Protection
```yaml
rules:
  - patterns: ["*.env", "**/*.env.*"]
    action: "block_deletion"
```

### Team-Based Overrides
```yaml
overrides:
  - policy_name: "env-protection"
    scope:
      teams: ["platform-team"]
    enforcement: "warn"
```

### Repository-Specific Policies
```yaml
scope:
  repositories: ["critical-*", "*-api"]
  branches: ["main", "production"]
```

## 📈 Scale & Performance

### Large Team Optimizations
- **Pattern Caching**: Compiled regex patterns cached
- **Hierarchical Evaluation**: Priority-based policy layering
- **Team Overrides**: Efficient team-based rule customization
- **Repository Scoping**: Targeted policy application

### Performance Metrics
```
Target Performance (1000+ developers):
- Evaluation Time: < 100ms per file
- Cache Hit Ratio: > 80%
- Hot Reload Time: < 500ms
- Memory Usage: < 100MB baseline
```

## 🔧 Configuration Schema

### Policy Structure
```yaml
version: "1.0"
metadata:
  name: string
  description: string
  author: string

templates: []PolicyTemplate
policies: []PolicyDefinition
overrides: []PolicyOverride
```

### Policy Definition
```yaml
policies:
  - name: string
    description: string
    enabled: boolean
    type: "file_protection"
    priority: integer (0-1000)
    scope:
      branches: []string
      repositories: []string
      teams: []string
      users: []string
    rules: []PolicyRule
    enforcement: "strict" | "warn" | "disabled"
```

### Rule Definition
```yaml
rules:
  - name: string
    type: "file_protection"
    patterns: []string
    action: "block_deletion" | "require_approval" | "warn"
    enforcement: "strict" | "warn"
    metadata: map[string]interface{}
```

## 🌐 API Reference

### Core Endpoints

| Endpoint | Method | Description |
|----------|---------|-------------|
| `/api/policy/status` | GET | Policy engine status |
| `/api/policy/evaluate` | GET | Evaluate file protection |
| `/api/policy/hierarchy` | GET | Layer status information |
| `/api/policy/templates` | GET | List available templates |
| `/api/policy/validate` | POST | Validate policy configuration |
| `/api/policy/reload` | POST | Manual policy reload |

### Example Requests

```bash
# Get status
curl localhost:8080/api/policy/status

# Evaluate file
curl "localhost:8080/api/policy/evaluate?file=config/prod.env"

# Validate policy
curl -X POST localhost:8080/api/policy/validate \
  -H "Content-Type: application/yaml" \
  --data-binary @policy.yml
```

## 🔄 Migration from Legacy

### Step 1: Enable Feature Flag
```bash
export COLLABOPS_POLICY_ENGINE_ENABLED=true
```

### Step 2: Create Policy Configuration
Convert hard-coded patterns to YAML policies:

```go
// Before: Hard-coded in Go
environmentPatterns := []string{"*.env", "config/**/*.yml"}

// After: YAML policy
```yaml
rules:
  - patterns: ["*.env", "config/**/*.yml"]
    action: "block_deletion"
```

### Step 3: Gradual Rollout
- Start with non-production environments
- Monitor policy violations and adjust
- Expand to production repositories
- Remove legacy hard-coded logic

### Step 4: Validation
```bash
# Verify both systems produce same results
# Test critical file protection scenarios
# Monitor performance impact
```

## 🏢 Enterprise Deployment

### Production Checklist
- [ ] Policy configuration validated
- [ ] Monitoring and alerting configured
- [ ] Emergency bypass procedures documented
- [ ] Team training completed
- [ ] Gradual rollout plan executed
- [ ] Performance benchmarks met

### Monitoring Setup
```yaml
alerts:
  - name: PolicyEngineDown
    condition: policy_engine_status != 1
  - name: PolicyViolationSpike
    condition: increase(policy_violations_total[5m]) > 100
  - name: PerformanceDegradation
    condition: policy_evaluation_duration > 200ms
```

## 📞 Support & Community

### Getting Help
1. **Documentation**: Check relevant guide in this docs/
2. **Troubleshooting**: See [troubleshooting.md](troubleshooting.md)
3. **Platform Team**: Contact your platform team
4. **Emergency**: Use emergency bypass procedures

### Contributing
- Policy configuration improvements
- Documentation updates
- Bug reports and feature requests
- Performance optimizations

### Links
- **Source Code**: `internal/policy/`
- **Examples**: `examples/phase2-advanced-policies.yml`
- **Tests**: `internal/policy/*_test.go`

---

**🎯 Mission**: Replace hard-coded protection logic with GitLab/GitHub-style Policy-as-Code framework for large-scale platforms.

**✅ Status**: Phase 2 Complete - Production Ready

**📧 Contact**: platform-team@company.com