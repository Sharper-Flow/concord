package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/sharper-flow/concord/internal/store"
)

// reconstructWorkerEvidenceAck rebuilds the event an exact acknowledgment
// (CD-0208 D1) must equal. The CLI transport exposes no user payload
// version — the request route stamps the registry's current version — so the
// comparison event is reconstructed at the stored event's own raw version.
// The stored row is never upcast or modified for the comparison, and new
// appends still stamp the registry current version.
//
// A stored dispatch whose original shape carries a lane_actor_ref (issue
// #800 / CD-0017 D4) is re-derived through the same store helper the live
// boundary uses, from the authenticated principal and client, so the exact
// match still proves the caller's identity equals the actor that recorded
// the dispatch. A dispatch that predates the enrichment compares its
// un-enriched shape unchanged.
func reconstructWorkerEvidenceAck(ctx context.Context, tx *store.Transaction, existing, requested store.Event, principalRef, clientRef string) (store.Event, error) {
	if existing.Kind == store.WorkerDispatched && requested.Kind == store.WorkerDispatched {
		var original store.WorkerDispatchedPayload
		if err := json.Unmarshal(existing.Payload, &original); err != nil {
			return store.Event{}, err
		}
		if original.LaneActorRef != "" {
			prepared, err := store.PrepareLaneActorDispatch(ctx, tx, requested, principalRef, clientRef)
			if err != nil {
				return store.Event{}, err
			}
			requested = prepared[len(prepared)-1]
		}
	}
	requested.PayloadVersion = existing.PayloadVersion
	return requested, nil
}

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
