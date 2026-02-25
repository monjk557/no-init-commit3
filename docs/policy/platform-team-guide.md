# Platform Team Operations Guide

## 🎯 Overview

Comprehensive guide for platform teams managing Policy-as-Code Framework in a 1000+ developer environment with go-git integration.

## 🚀 Initial Setup

### 1. Environment Configuration

```bash
# Production environment
export COLLABOPS_POLICY_ENGINE_ENABLED=true
export COLLABOPS_LOG_LEVEL=info
export COLLABOPS_POLICY_PATH=/etc/collabops/policies
export COLLABOPS_HOT_RELOAD=true

# Development/staging
export COLLABOPS_POLICY_ENGINE_ENABLED=true
export COLLABOPS_LOG_LEVEL=debug
export COLLABOPS_POLICY_PATH=./configs/policies
export COLLABOPS_HOT_RELOAD=true
```

### 2. Directory Structure

```
/etc/collabops/policies/
├── environment-protection.yml    # Core environment file protection
├── infrastructure.yml           # Infrastructure and deployment files
├── security.yml                # Security-sensitive files
└── development.yml             # Development environment guidelines
```

### 3. Monitoring Setup

```yaml
# Add to your monitoring stack
alerts:
  - name: "PolicyEngineDown"
    condition: "policy_engine_status != 1"
    severity: "critical"

  - name: "PolicyViolationSpike"
    condition: "increase(policy_violations_total[5m]) > 100"
    severity: "warning"

  - name: "PolicyReloadFailure"
    condition: "policy_reload_failures_total > 0"
    severity: "warning"
```

## 📊 Production-Ready Policy Configuration

### Core Environment Protection

```yaml
# /etc/collabops/policies/environment-protection.yml
version: "1.0"
metadata:
  name: "Production Environment Protection"
  description: "Core policies for 1000+ developer platform"
  author: "platform-team@company.com"
  version: "1.0"

policies:
  - name: "critical-env-protection"
    description: "Protect critical environment files"
    enabled: true
    type: "file_protection"
    priority: 1000
    scope:
      branches: ["main", "master", "production", "release/*"]
      repositories: ["*"]
    rules:
      - name: "production-secrets"
        type: "file_protection"
        patterns:
          # Production environment files
          - "*.env.production"
          - "*.env.prod"
          - ".env.production"
          - ".env.prod"
          # Critical config directories
          - "config/production/**/*"
          - "environments/production/**/*"
          - "secrets/**/*"
          # Kubernetes secrets
          - "k8s/production/**/*.yaml"
          - "k8s/production/**/*.yml"
        action: "block_deletion"
        enforcement: "strict"
        metadata:
          severity: "critical"
          team: "platform"

  - name: "staging-env-protection"
    description: "Protect staging environment files"
    enabled: true
    type: "file_protection"
    priority: 900
    scope:
      branches: ["develop", "staging", "release/*"]
    rules:
      - name: "staging-configs"
        type: "file_protection"
        patterns:
          - "*.env.staging"
          - "*.env.stage"
          - "config/staging/**/*"
          - "environments/staging/**/*"
        action: "require_approval"
        enforcement: "strict"
        metadata:
          approvers: ["platform-team"]

  - name: "general-env-guidelines"
    description: "General environment file guidelines"
    enabled: true
    type: "file_protection"
    priority: 100
    scope:
      branches: ["*"]
    rules:
      - name: "env-files"
        type: "file_protection"
        patterns:
          - "*.env"
          - "*.env.*"
          - ".env"
          - ".env.*"
        action: "warn"
        enforcement: "warn"
        metadata:
          message: "Environment file modified - ensure no secrets are committed"
```

### Infrastructure Protection

```yaml
# /etc/collabops/policies/infrastructure.yml
version: "1.0"
metadata:
  name: "Infrastructure Protection"
  description: "Protect infrastructure and deployment configurations"

policies:
  - name: "terraform-protection"
    enabled: true
    type: "file_protection"
    priority: 800
    scope:
      repositories: ["infrastructure-*", "*-terraform", "platform-*"]
    rules:
      - name: "terraform-files"
        patterns:
          - "**/*.tf"
          - "**/*.tfvars"
          - "terraform.tfstate*"
        action: "require_approval"
        metadata:
          approvers: ["platform-team", "devops-team"]
          min_required: 2

  - name: "k8s-manifests-protection"
    enabled: true
    type: "file_protection"
    priority: 700
    rules:
      - name: "k8s-production"
        patterns:
          - "k8s/production/**/*"
          - "manifests/production/**/*"
          - "deployments/production/**/*"
        action: "block_deletion"
        scope:
          branches: ["main", "production"]

      - name: "k8s-general"
        patterns:
          - "k8s/**/*.yaml"
          - "k8s/**/*.yml"
          - "*.k8s.yaml"
        action: "warn"
```

## 🔧 Team-Specific Overrides

### Large Team Management

```yaml
# /etc/collabops/policies/team-overrides.yml
overrides:
  # Platform team - full access
  - policy_name: "*"
    scope:
      teams: ["platform-team", "devops-team"]
      users: ["platform-admin@company.com"]
    enforcement: "warn"  # Warnings only, no blocks

  # Senior engineers - reduced restrictions
  - policy_name: "general-env-guidelines"
    scope:
      teams: ["tech-leads", "senior-engineers"]
    enforcement: "warn"

  # CI/CD automation
  - policy_name: "*"
    scope:
      users: ["ci-bot", "deployment-bot", "github-actions[bot]"]
    enabled: false  # Bots can bypass all policies

  # Emergency hotfix permissions
  - policy_name: "*"
    scope:
      branches: ["hotfix/*", "emergency/*"]
      teams: ["oncall-team"]
    enforcement: "warn"  # Allow emergency fixes with warnings

  # Test repositories - relaxed policies
  - policy_name: "*"
    scope:
      repositories: ["*-test", "*-sandbox", "playground-*", "demo-*"]
    enforcement: "disabled"
```

## 📈 Scaling for 1000+ Developers

### Performance Optimization

```yaml
# Engine configuration for large scale
engine_config:
  # Increase cache sizes
  pattern_cache_size: 10000
  policy_cache_ttl: "5m"

  # Optimize for high throughput
  evaluation_timeout: "100ms"
  concurrent_evaluations: 100

  # Hot reload settings
  hot_reload:
    debounce_interval: "1s"  # Increased for stability
    max_reload_frequency: "1/minute"
```

### Repository Categorization

```yaml
# Automatic repository classification
repository_patterns:
  critical_services:
    patterns: ["user-service", "payment-*", "auth-*", "*-api"]
    policies: ["critical-env-protection", "infrastructure-protection"]

  internal_tools:
    patterns: ["tool-*", "*-cli", "script-*"]
    policies: ["general-env-guidelines"]

  experimental:
    patterns: ["experiment-*", "poc-*", "research-*"]
    policies: []  # No restrictions
```

## 🚨 Incident Response

### Emergency Procedures

```bash
# 1. Disable all policies immediately (emergency)
export COLLABOPS_POLICY_ENGINE_ENABLED=false
# Or set in config file

# 2. Disable specific policy temporarily
curl -X POST "localhost:8080/api/policy/disable" \
  -d '{"policy_name": "critical-env-protection", "duration": "1h"}'

# 3. Emergency bypass for specific user
curl -X POST "localhost:8080/api/policy/override" \
  -d '{"user": "emergency-user", "duration": "30m", "reason": "INCIDENT-12345"}'
```

### Rollback Procedures

```bash
# 1. Rollback to previous policy version
cp /etc/collabops/policies/backup/environment-protection.yml.bak \
   /etc/collabops/policies/environment-protection.yml

# 2. Policy engine automatically reloads (hot reload)

# 3. Verify rollback
curl -s localhost:8080/api/policy/status | jq .last_reload
```

## 📊 Monitoring & Dashboards

### Key Metrics to Monitor

```yaml
# Core metrics for 1000+ developer platform
metrics:
  # Performance
  - policy_evaluation_duration_seconds
  - policy_evaluations_per_second
  - policy_cache_hit_ratio

  # Business metrics
  - policy_violations_by_repository
  - policy_violations_by_team
  - policy_violations_by_rule

  # Operational
  - policy_reload_frequency
  - policy_engine_uptime
  - policy_configuration_errors
```

### Grafana Dashboard Queries

```promql
# Policy violation rate by team
rate(policy_violations_total[5m]) by (team, repository)

# Top violating repositories
topk(10, sum(rate(policy_violations_total[24h])) by (repository))

# Policy engine performance
histogram_quantile(0.95, rate(policy_evaluation_duration_seconds_bucket[5m]))
```

## 🔄 Change Management

### Policy Change Process

1. **Proposal**: Create RFC for policy changes
2. **Review**: Security, Platform, and affected teams review
3. **Testing**: Deploy to staging environment first
4. **Gradual Rollout**: Deploy to subset of repositories
5. **Full Deployment**: Roll out to all repositories
6. **Monitor**: Watch for unexpected violations or performance impact

### Version Control

```bash
# Policy versioning structure
/etc/collabops/policies/
├── versions/
│   ├── v1.0/
│   ├── v1.1/
│   └── v2.0/
├── current -> versions/v2.0/
└── backup/
    └── environment-protection.yml.bak
```

## 🛡️ Security Considerations

### Sensitive Pattern Management

```yaml
# Avoid exposing sensitive patterns in policy files
rules:
  - name: "api-keys"
    patterns:
      - "config/api-keys/*"  # ✅ Good - directory based
      # ❌ Avoid: "**/prod-api-key-abc123.txt" - exposes actual key names
    action: "block_deletion"
```

### Access Control

```yaml
# Policy file permissions
chown platform-team:platform-team /etc/collabops/policies/*.yml
chmod 640 /etc/collabops/policies/*.yml

# API access control (if exposed)
api_access:
  read_only: ["developers"]
  read_write: ["platform-team"]
  admin: ["platform-admin"]
```

## 🎯 Best Practices Summary

### For 1000+ Developer Scale

1. **Start Simple**: Begin with basic environment protection
2. **Gradual Expansion**: Add policies incrementally based on actual needs
3. **Team Collaboration**: Work with security and development teams
4. **Monitor Impact**: Watch for productivity impact and adjust
5. **Documentation**: Keep policies well-documented and communicated
6. **Emergency Procedures**: Have clear escalation and override procedures

### Policy Design Guidelines

- **Specific Patterns**: Use specific file patterns, avoid overly broad matches
- **Clear Actions**: Use `block_deletion` for critical files, `warn` for guidelines
- **Team Overrides**: Provide appropriate overrides for different team roles
- **Repository Scoping**: Apply different policies based on repository criticality

---

**Platform Team Contact**: platform-team@company.com
**Emergency Escalation**: oncall-platform@company.com
**Documentation**: /internal/docs/policy-engine/