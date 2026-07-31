package ruleguard

import "fmt"

// Denied reports a blocking ruleset violation for one pushed ref. The push
// choke points (SSH pre-receive hook, HTTP receive-pack) turn this into the
// `ng <ref> <reason>` git report line.
//
// Rule evaluation is entirely local — see eval.go / engine.go. The executor
// deliberately holds no client to the control plane: a push must not depend on
// orchestrator reachability (design §10 Phase 3, rule-bundle.md).
type Denied struct {
	Branch string
	Reason string
}

func (d *Denied) Error() string {
	if d.Reason != "" {
		return d.Reason
	}
	return fmt.Sprintf("push to %s rejected by ruleset", d.Branch)
}
