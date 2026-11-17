package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// An unavailable store maps to a deliverable typed unreachable refusal.
func TestUnavailableStoreRefusalMarshalsAsUnreachable(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindUnavailable,
		Op:             "resolve_committed_law",
		Detail:         "cannot clear git-derived law_subjects",
		RetrySafe:      true,
		RecoveryAction: "retry once the database is writable",
	}
	base := agentcontract.NewBase("concord_domain", "list")
	out := failureEnvelope(base, failure)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("error=%+v, want kind unreachable", out.Error)
	}
	if out.Result != nil || out.Items != nil {
		t.Fatalf("unreachable refusal carries payload: result=%s items=%v", out.Result, out.Items)
	}
	withResult, withItems := out, out
	withResult.Result = json.RawMessage(`{}`)
	withItems.Items = []json.RawMessage{json.RawMessage(`{}`)}
	for _, poisoned := range []agentcontract.Envelope{withResult, withItems} {
		if _, err := poisoned.Encode(); err == nil {
			t.Fatal("unreachable refusal accepted a payload")
		}
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("an unavailable-store refusal must be deliverable, got %v", err)
	}
	if out.Error.Message != failure.Detail || !out.Error.RetrySafe {
		t.Fatalf("refusal detail lost: %+v", out.Error)
	}
}

// An unreachable refusal names the database error that caused it. Without the
// cause, "cannot begin transaction" reads the same for lock contention, a full
// disk, and a corrupt file, and the caller cannot choose between a retry and an
// escalation.
func TestUnavailableStoreRefusalCarriesItsCause(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindUnavailable,
		Op:             "transaction",
		Detail:         "cannot begin transaction",
		RetrySafe:      true,
		RecoveryAction: "retry once the database is writable",
		Err:            errors.New("database is locked (5) (SQLITE_BUSY)"),
	}
	out := failureEnvelope(agentcontract.NewBase("concord_work_transition", "workflow_action"), failure)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("error=%+v, want kind unreachable", out.Error)
	}
	if want := "cannot begin transaction: database is locked (5) (SQLITE_BUSY)"; out.Error.Message != want {
		t.Fatalf("message=%q, want %q", out.Error.Message, want)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("a refusal with its cause must be deliverable, got %v", err)
	}
}

// A post-commit failure whose wrapped cause is a deadline keeps its possible
// effect. The deadline branch reads "no commit happened"; a store failure
// marked EffectPossible followed a committed transaction (CD-0050 D2), so
// reporting it as a clean timeout tells the caller no effect exists when the
// effect stands.
func TestEffectPossibleFailureWithDeadlineCauseKeepsPossibleEffect(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindUnavailable,
		Op:             "sync_durable",
		Detail:         "durability checkpoint did not complete",
		RetrySafe:      true,
		EffectPossible: true,
		Err:            context.DeadlineExceeded,
	}
	out := failureEnvelope(agentcontract.NewBase("concord_work_transition", "workflow_action"), failure)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("error=%+v, want kind unreachable", out.Error)
	}
	if out.Error.EffectState != agentcontract.EffectPossible {
		t.Fatalf("effect state=%q, want possible", out.Error.EffectState)
	}
	if out.Error.Message == "operation budget expired" {
		t.Fatal("a committed effect read as a clean timeout with no effect")
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("the refusal must be deliverable, got %v", err)
	}
}

func TestCoreErrorOtherKindsMarshal(t *testing.T) {
	t.Parallel()
	out := coreError(agentcontract.NewBase("concord_domain", "list"), "unknown_scope", "no Product", "contact_operator", false)
	if out.Error == nil || out.Error.Kind != "unknown_scope" {
		t.Fatalf("error=%+v, want unknown_scope", out.Error)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatal(err)
	}
}
