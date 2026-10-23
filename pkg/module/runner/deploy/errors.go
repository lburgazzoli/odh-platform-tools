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

// Stage names the deployment phase an error refers to.
type Stage string

const (
	// StagePrepare covers the checks made before the action runs:
	// invocation validity and the persisted owner's runtime-type
	// normalization.
	StagePrepare Stage = "prepare"
	// StageApply covers the single framework action invocation.
	StageApply Stage = "apply"
)

// Policy sentinels. They carry identity through the typed error's
// cause chain for errors.Is and errors.As; none of their text echoes
// API-returned values.
var (
	// errNilArgument marks a nil Apply argument.
	errNilArgument = errors.New("nil deploy argument")
)

// Error is the safe error Apply returns: the failing stage, a message
// that never echoes server-returned text, and the raw cause — the
// framework's own report — reachable only through Unwrap for
// errors.Is and errors.As.
type Error struct {
	// Stage is the deployment phase that failed.
	Stage Stage
	// Message is safe text: it names the stage and operation, never
	// the server's own error body.
	Message string

	cause error
}

// Error returns the safe stage message.
func (e *Error) Error() string {
	return e.Message
}

// Unwrap returns the raw cause — the framework action's first error
// report — so callers can classify the underlying failure.
func (e *Error) Unwrap() error {
	return e.cause
}

// stageMessages are the safe operation summaries per stage. They
// intentionally omit server-returned detail, which stays reachable
// through the cause chain.
var stageMessages = map[Stage]string{
	StagePrepare: "preparing the module deployment failed; the raw causes remain reachable through errors.Is and errors.As",
	StageApply:   "applying the module resources failed; the raw causes remain reachable through errors.Is and errors.As",
}
