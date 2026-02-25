# Policy Engine User Guide

## 🎯 Overview

This guide helps developers and platform teams use the Policy-as-Code Framework in a large-scale environment (1000+ developers) with go-git integration.

## 🚀 Quick Start

### For Developers

The policy engine protects critical files automatically. No action needed for most workflows.

```bash
# Your normal git operations work as usual
git add .
git commit -m "Update feature"
git push

# Policy engine runs automatically during merge operations
# Protected files (*.env, config files, etc.) are preserved
```

### For Platform Teams

Enable the policy engine with environment variable:

```bash
export COLLABOPS_POLICY_ENGINE_ENABLED=true
```

## 📁 Basic Configuration

### Simple Environment Protection

Create `/configs/policies/environment-protection.yml`:

```yaml
version: "1.0"
metadata:
  name: "Basic Environment Protection"
  description: "Protects environment files for 1000+ developer team"

policies:
  - name: "env-file-protection"
    description: "Protect environment and config files"
    enabled: true
    type: "file_protection"
    priority: 100
    scope:
      branches: ["main", "master", "production"]
      repositories: ["*"]
    rules:
      - name: "environment-files"
        type: "file_protection"
        patterns:
          - "*.env"
          - "*.env.*"
          - "config/**/*.env"
          - ".env.production"
          - ".env.staging"
        action: "block_deletion"
        enforcement: "strict"

      - name: "config-files"
        type: "file_protection"
        patterns:
          - "config/**/*.yml"
          - "config/**/*.yaml"
          - "docker-compose*.yml"
          - "k8s/**/*.yaml"
        action: "warn"
        enforcement: "warn"
    enforcement: "strict"
```

## 🏢 Large Team Scenarios

### Repository-Specific Policies

```yaml
policies:
  # Critical production services
  - name: "production-services"
    scope:
      repositories: ["user-service", "payment-service", "auth-service"]
      branches: ["main", "production"]
    rules:
      - patterns: ["*.env", "config/**/*"]
        action: "block_deletion"

  # Development/testing repos - more relaxed
  - name: "development-repos"
    scope:
      repositories: ["*-dev", "*-test", "sandbox-*"]
    rules:
      - patterns: ["*.env"]
        action: "warn"  # Just warn, don't block
```

### Team-Based Overrides

```yaml
overrides:
  # Platform team can bypass some restrictions
  - policy_name: "env-file-protection"
    scope:
      teams: ["platform-team", "devops-team"]
    enforcement: "warn"

  # CI/CD bots need special access
  - policy_name: "env-file-protection"
    scope:
      users: ["ci-bot", "deployment-bot"]
    enabled: false
```

## 💼 Common Use Cases

### 1. Protecting Production Configurations

```yaml
policies:
  - name: "production-config-protection"
    priority: 1000  # Highest priority
    scope:
      branches: ["main", "production"]
    rules:
      - name: "prod-secrets"
        patterns:
          - "environments/production/**/*"
          - "*.prod.env"
          - "k8s/production/**/*"
        action: "block_deletion"
```

### 2. Development Environment Guidelines

```yaml
policies:
  - name: "dev-guidelines"
    priority: 100
    scope:
      branches: ["develop", "feature/*"]
    rules:
      - name: "dev-configs"
        patterns: ["*.env.dev", "config/development/**/*"]
        action: "warn"
        metadata:
          message: "Development config changed - verify staging compatibility"
```

### 3. Infrastructure Protection

```yaml
policies:
  - name: "infrastructure-protection"
    rules:
      - name: "terraform-files"
        patterns: ["**/*.tf", "**/*.tfvars"]
        action: "require_approval"
        metadata:
          approvers: ["platform-team"]

      - name: "k8s-manifests"
        patterns: ["k8s/**/*", "deployments/**/*"]
        action: "require_approval"
```

## 🔧 Integration with go-git

The policy engine integrates seamlessly with your existing go-git workflows:

```go
// In your merge operation code
func (m *MergeHandler) HandleMerge(ctx context.Context, req *MergeRequest) error {
    // Policy engine automatically evaluates during merge
    decision, err := m.policyEngine.EvaluateMergePolicy(ctx, req)
    if err != nil {
        return fmt.Errorf("policy evaluation failed: %w", err)
    }

    if !decision.Allowed {
        return fmt.Errorf("merge blocked by policy: %v", decision.BlockingRules)
    }

    // Proceed with normal merge operation
    return m.performMerge(ctx, req)
}
```

## 📊 Monitoring & Operations

### Health Check

```bash
# Check policy engine status
curl -s localhost:8080/api/policy/status | jq

# Expected response:
{
  "initialized": true,
  "policies_loaded": 5,
  "rules_loaded": 15,
  "last_reload": "2026-02-25T09:00:00Z"
}
```

### Hot Reload

Policy changes are automatically detected and reloaded:

```bash
# Edit policy file
vim /configs/policies/environment-protection.yml

# Changes applied automatically within 500ms
# Check logs for confirmation:
tail -f /var/log/collabops/policy-engine.log
```

### Performance Monitoring

For 1000+ developer teams, monitor these metrics:

```yaml
# Add to your monitoring dashboard
metrics:
  - policy_evaluations_total
  - policy_evaluation_duration_seconds
  - policy_violations_total
  - policy_reload_duration_seconds
```

## 🚨 Troubleshooting

### Common Issues

#### 1. Policy Engine Not Loading
```bash
# Check environment variable
echo $COLLABOPS_POLICY_ENGINE_ENABLED

# Check policy file exists
ls -la /configs/policies/

# Check logs
tail -f /var/log/collabops/policy-engine.log
```

#### 2. Files Unexpectedly Protected
```bash
# Debug specific file
curl -s "localhost:8080/api/policy/evaluate?file=config/test.env" | jq

# Shows which policy/rule matched
{
  "protected": true,
  "reason": "file matches protected pattern '*.env' in policy 'env-file-protection'",
  "matched_rule": "environment-files"
}
```

#### 3. Performance Issues
```yaml
# Optimize patterns - avoid overly broad matches
patterns:
  - "*.env"              # ✅ Good
  - "config/**/*.env"    # ✅ Good
  - "**/*"              # ❌ Too broad, will be rejected
```

## 🎯 Best Practices for Large Teams

### 1. Repository Organization
```
your-company/
├── policies/
│   ├── common.yml          # Shared policies
│   ├── production.yml      # Production-specific
│   └── development.yml     # Development guidelines
├── service-a/
├── service-b/
└── platform-tools/
```

### 2. Gradual Rollout
```bash
# Phase 1: Enable for critical repos only
COLLABOPS_POLICY_ENGINE_ENABLED=true
POLICY_SCOPE="critical-services"

# Phase 2: Expand to all production repos
POLICY_SCOPE="production"

# Phase 3: Enable organization-wide
POLICY_SCOPE="*"
```

### 3. Policy Governance
- **Policy Reviews**: Treat policy changes like code reviews
- **Version Control**: Keep policies in git with proper versioning
- **Documentation**: Document policy decisions and exceptions
- **Regular Audits**: Review policy violations and adjust as needed

## 📞 Support

### Getting Help

1. **Documentation**: Check this guide and examples/
2. **Logs**: Check policy engine logs for detailed error information
3. **Platform Team**: Contact your platform team for policy questions
4. **Emergency**: For urgent production issues, platform team can temporarily disable policies

### Feature Requests

The policy engine is designed to be simple but effective for large teams. For additional features, discuss with your platform team.

---

**Policy Engine Version**: 2.0
**Last Updated**: February 2026
**Team Size**: Optimized for 1000+ developers