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
	// The core-derived terminal members are producer-owned. The comparison
	// carries the stored event's own consumed_context_frontier and
	// observed_delivery beside the caller's content, so a replay never
	// re-derives — and never re-observes — the identity the store recorded,
	// while every caller-owned byte still compares. A stored event recorded
	// before the members existed carries none, and the comparison expects
	// none.
	if existing.Kind == store.WorkerCompleted || existing.Kind == store.WorkerFailed {
		merged, err := mergeWorkerTerminalProducerFields(existing, requested)
		if err != nil {
			return store.Event{}, err
		}
		requested = merged
	}
	requested.PayloadVersion = existing.PayloadVersion
	return requested, nil
}

// mergeWorkerTerminalProducerFields copies the stored event's core-derived
// members onto the comparison payload as raw JSON. The stored payload is the
// authority for what the store derived; a member it does not carry stays
// absent, and a caller-supplied member can never stand in for one.
//
// The failure_attribution identity is core-derived the same way :
// the caller's request carries the producer observation only, so the stored
// event's derived identity is overlaid onto the comparison's attribution
// while the caller's observation bytes still compare. A stored identity
// beside a request without attribution refuses, as does the reverse.
func mergeWorkerTerminalProducerFields(existing, requested store.Event) (store.Event, error) {
	var storedMembers struct {
		ConsumedContextFrontier *int64                        `json:"consumed_context_frontier"`
		ObservedDelivery        *store.WorkerObservedDelivery `json:"observed_delivery"`
		FailureAttribution      *struct {
			Identity json.RawMessage `json:"identity"`
		} `json:"failure_attribution"`
	}
	if err := json.Unmarshal(existing.Payload, &storedMembers); err != nil {
		return store.Event{}, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(requested.Payload, &members); err != nil {
		return store.Event{}, err
	}
	delete(members, "consumed_context_frontier")
	delete(members, "observed_delivery")
	if storedMembers.ConsumedContextFrontier != nil {
		raw, err := json.Marshal(storedMembers.ConsumedContextFrontier)
		if err != nil {
			return store.Event{}, err
		}
		members["consumed_context_frontier"] = raw
	}
	if storedMembers.ObservedDelivery != nil {
		raw, err := json.Marshal(storedMembers.ObservedDelivery)
		if err != nil {
			return store.Event{}, err
		}
		members["observed_delivery"] = raw
	}
	if existing.Kind == store.WorkerCompleted {
		var original map[string]json.RawMessage
		if err := json.Unmarshal(existing.Payload, &original); err != nil {
			return store.Event{}, err
		}
		var prior, next []map[string]json.RawMessage
		if err := json.Unmarshal(original["evidence"], &prior); err != nil {
			return store.Event{}, err
		}
		if err := json.Unmarshal(members["evidence"], &next); err != nil {
			return store.Event{}, err
		}
		if len(prior) == len(next) {
			for i, entry := range next {
				var storedReceipt, requestedReceipt map[string]json.RawMessage
				if json.Unmarshal(prior[i]["oracle_receipt"], &storedReceipt) != nil || json.Unmarshal(entry["oracle_receipt"], &requestedReceipt) != nil || requestedReceipt == nil {
					continue
				}
				// Only the omitted producer subject is overlaid. Every claimed
				// subject and every other receipt member still compares exactly.
				if _, claimed := requestedReceipt["subject_commit"]; !claimed && len(storedReceipt["subject_commit"]) > 0 {
					requestedReceipt["subject_commit"] = storedReceipt["subject_commit"]
					raw, err := json.Marshal(requestedReceipt)
					if err != nil {
						return store.Event{}, err
					}
					entry["oracle_receipt"] = raw
				}
			}
			raw, err := json.Marshal(next)
			if err != nil {
				return store.Event{}, err
			}
			members["evidence"] = raw
		}
	}
	if storedMembers.FailureAttribution != nil && len(storedMembers.FailureAttribution.Identity) > 0 {
		requestedAttribution, ok := members["failure_attribution"]
		if !ok {
			return store.Event{}, errors.New("worker evidence replay omits the failure_attribution the stored event records")
		}
		var attributionMembers map[string]json.RawMessage
		if err := json.Unmarshal(requestedAttribution, &attributionMembers); err != nil {
			return store.Event{}, err
		}
		attributionMembers["identity"] = storedMembers.FailureAttribution.Identity
		raw, err := json.Marshal(attributionMembers)
		if err != nil {
			return store.Event{}, err
		}
		members["failure_attribution"] = raw
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return store.Event{}, err
	}
	requested.Payload = raw
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
