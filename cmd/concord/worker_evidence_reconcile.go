package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/sharper-flow/concord/internal/store"
)

// An exact reconciliation authenticates a fresh assertion, but appends no new
// evidence. Occurrence time is not payload identity: a retry observes a new time.
func sameWorkerEvidence(existing, requested store.Event) bool {
	if existing.Kind != requested.Kind || existing.SubjectType != requested.SubjectType || existing.SubjectID != requested.SubjectID || existing.Actor != requested.Actor || existing.PayloadVersion != requested.PayloadVersion {
		return false
	}
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	prior, err := decode(existing.Payload)
	if err != nil {
		return false
	}
	next, err := decode(requested.Payload)
	return err == nil && reflect.DeepEqual(prior, next)
}

// The adapter reads this closed result, rather than guessing effect state from
// stderr. Human diagnostics retain the original error text on stderr.
func writeWorkerEvidenceFailure(out, errOut io.Writer, err error, eventIDs []string) {
	detail := struct {
		Kind        string `json:"kind"`
		Operation   string `json:"operation"`
		RetrySafe   bool   `json:"retry_safe"`
		EffectState string `json:"effect_state"`
		Message     string `json:"message"`
	}{Kind: "invalid_input", Operation: "worker_evidence", EffectState: "none", Message: err.Error()}
	var failure *store.Failure
	if errors.As(err, &failure) {
		detail.Kind = string(failure.Kind)
		detail.Operation = failure.Op
		detail.RetrySafe = failure.RetrySafe
		if failure.EffectPossible {
			detail.EffectState = "possible"
		}
	}
	_ = writeJSON(out, struct {
		OK       bool     `json:"ok"`
		EventIDs []string `json:"event_ids"`
		Error    any      `json:"error"`
	}{EventIDs: eventIDs, Error: detail}, errOut)
}
