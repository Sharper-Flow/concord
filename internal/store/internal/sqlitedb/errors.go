package sqlitedb

import "fmt"

// Error describes a connection or transaction mechanism failure. The store
// boundary translates it into its public failure envelope.
type Error struct {
	Kind           ErrorKind
	Op             string
	Detail         string
	RetrySafe      bool
	RecoveryAction string
	EffectPossible bool
	Err            error
}

type ErrorKind string

const (
	Unavailable        ErrorKind = "unavailable"
	InvalidOperation   ErrorKind = "invalid_operation"
	InvariantViolation ErrorKind = "invariant_violation"
)

func (e *Error) Error() string {
	message := fmt.Sprintf("store: %s: %s: %s", e.Op, e.Kind, e.Detail)
	if e.Err != nil {
		message += fmt.Sprintf(": %v", e.Err)
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

func failure(kind ErrorKind, op, detail string, retry bool, recovery string, err error) *Error {
	return &Error{Kind: kind, Op: op, Detail: detail, RetrySafe: retry, RecoveryAction: recovery, Err: err}
}
