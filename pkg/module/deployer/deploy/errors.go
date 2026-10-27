// Package deploy installs rendered module resources through the
// actual framework deploy action.
//
// The boundary is Apply: it receives the server-backed definition
// owner, the framework release, and the rendered resources, and
// applies the whole payload through one framework action invocation —
// never publishing an owner itself and never pruning. The framework's
// continue-on-error first report is the stage's error contract and its
// force-SSA adoption semantics govern foreign-owned objects, exactly
// as in the operator.
package deploy

import "errors"

// errNilArgument marks a nil Apply argument; it carries identity
// through the shared error's cause chain for errors.Is and its text
// never echoes API-returned values.
var errNilArgument = errors.New("nil deploy argument")

// Fixed safe operation summaries, one per phase. They intentionally
// omit server-returned detail, which stays reachable through the
// cause chain: failures before the action runs are classified by
// moduleerrors.ErrDeployPrepare, action failures by
// moduleerrors.ErrDeployApply.
const (
	prepareFailureMessage = "preparing the module deployment failed; " +
		"the raw causes remain reachable through errors.Is and errors.As"
	applyFailureMessage = "applying the module resources failed; " +
		"the raw causes remain reachable through errors.Is and errors.As"
)
