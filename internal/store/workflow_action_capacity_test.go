package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// The whole-action decode budget is derived, not guessed: the capacity tests
// walk the generated payload contract's declared maxima for every part a
// worker packet can carry through the shared test-support witness, apply the
// store-owned bounds the schema leaves to the writers, and prove the
// constant holds the composition. A later part increase fails here instead
// of at the transport, and any member the schema leaves unbounded fails the
// walk with its exact path until its owner is inventoried.

// TestCapacityWalkerWireTruth pins the wire arithmetic the derivation rests
// on: maxLength bounds code points, not serialized bytes, and every
// permitted scalar takes six encoded bytes — control escapes, Go HTML
// escapes of < > &, raw astral UTF-8, and two-byte \n-style escapes all
// stay at or under six — inside two quotes. No pattern cheapens the figure:
// an unanchored pattern admits scalars it never names, and object and array
// syntax wrappers count with their property names and delimiters.
func TestCapacityWalkerWireTruth(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	opt := schemawire.Options{Defs: defs}
	cases := []struct {
		name string
		node map[string]any
		want int
	}{
		{"maxLength one admits U+0000, which escapes to six bytes in quotes", map[string]any{"type": "string", "maxLength": 1}, 8},
		{"an unproven 64-code-point string escapes to six bytes per scalar", map[string]any{"type": "string", "maxLength": 64}, 2 + 64*6},
	}
	for _, tc := range cases {
		got, err := schemawire.Max(tc.node, "wire-truth/"+tc.name, opt)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got < tc.want {
			t.Fatalf("%s: walker measured %d bytes, want at least the wire truth %d (maxLength counts code points, not serialized bytes)", tc.name, got, tc.want)
		}
	}
	// The escaped-six-byte bound is unconditional: no pattern analysis can
	// cheapen it. An unanchored pattern admits scalars the pattern never
	// names ('💩a' matches pattern "a"), '<' HTML-escapes to six bytes even
	// inside an ASCII class, and '\n' serializes as a two-byte escape.
	unanchored := map[string]any{"type": "string", "maxLength": 10, "pattern": "a"}
	got, err := schemawire.Max(unanchored, "wire-truth/unanchored", opt)
	if err != nil {
		t.Fatal(err)
	}
	if got < 2+10*6 {
		t.Fatalf("unanchored pattern 'a' admits '💩aaaaaaaaa' (%d serialized bytes in quotes); the walker measured %d", 2+4+9, got)
	}
	html := map[string]any{"type": "string", "maxLength": 1, "pattern": "^[<]$"}
	if got, err = schemawire.Max(html, "wire-truth/html", opt); err != nil {
		t.Fatal(err)
	}
	if got < 2+6 {
		t.Fatalf("pattern '^[<]$' admits '<', which the encoder escapes to six bytes in quotes; the walker measured %d", got)
	}
	newline := map[string]any{"type": "string", "maxLength": 1, "pattern": "^\\s$"}
	if got, err = schemawire.Max(newline, "wire-truth/newline", opt); err != nil {
		t.Fatal(err)
	}
	if got < 2+2 {
		t.Fatalf("pattern '^\\\\s$' admits '\\n', which serializes as a two-byte escape in quotes; the walker measured %d", got)
	}
	object := map[string]any{"type": "object", "properties": map[string]any{
		"a": map[string]any{"type": "string", "maxLength": 1, "pattern": "^[a]$"},
		"b": map[string]any{"type": "array", "maxItems": 2, "items": map[string]any{"type": "string", "maxLength": 1, "pattern": "^[b]$"}},
	}}
	// Every one-code-point scalar at its six-byte encoded worst:
	// {"a":"<6>","b":["<6>","<6>"]} = braces 2 + name 4 + colon 1 + value 8
	// + comma 1 + name 4 + colon 1 + array 19 + brace 1 = 38 bytes.
	got, err = schemawire.Max(object, "wire-truth/object", opt)
	if err != nil {
		t.Fatal(err)
	}
	if got != 38 {
		t.Fatalf("object wire truth = %d bytes, want the 38 serialized bytes including property names and delimiters", got)
	}
}

// TestWorkflowActionPayloadBudgetDerivesFromDeclaredMaxima composes the
// declared maxima of the worker packet and proves the whole-action decode
// budget holds them. The three context members contribute the context
// aggregate exactly once — the aggregate already includes checkpoint,
// correction, and the whole view together with their member names and
// delimiters, so the walk excludes the members and injects the aggregate
// minus its own two braces plus the one comma the excluded members
// surrender. The acceptance oracle contributes the store's own 32 KiB bound,
// which the generated schema leaves to the writer, and the writer-stamped
// date-times contribute the core writer's RFC3339Nano UTC serialization
// (workflow.go design and proposal inserts).
func TestWorkflowActionPayloadBudgetDerivesFromDeclaredMaxima(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	// The finding-reference owner condition: the event sequence is a
	// non-negative int64 (nineteen digits), and at most sixteen declared
	// findings per anchor keep the ordinal at two digits.
	if WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", WorkContextActionFindingsMax)
	}
	// Quotes count: the reference serializes inside quotation marks.
	findingRefMax := 2 + len("finding:") + 19 + 1 + 2
	opt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			// The serialized acceptance oracle is bounded by its writer,
			// not by the generated schema.
			"worker_packet2/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": OracleCombinedMaxBytes,
			// The writer-stamped date-times carry the core writer's
			// RFC3339Nano UTC serialization.
			"worker_packet2/inputs/design_record/workflow_design_record/recorded_at":     32,
			"worker_packet2/inputs/proposal_record/workflow_proposal_record/recorded_at": 32,
		},
		Excludes: map[string]bool{
			"worker_packet2/inputs/work_context": true,
			"worker_packet2/inputs/checkpoint":   true,
			"worker_packet2/inputs/correction":   true,
		},
		FindingRefMax: findingRefMax,
	}
	packetParts, err := schemawire.Max(defs["worker_packet2"], "worker_packet2", opt)
	if err != nil {
		t.Fatalf("the packet derivation hit an unbudgeted member: %v", err)
	}
	// The three excluded members contribute the context aggregate minus its
	// own two braces plus the one comma the exclusion surrenders.
	composed := packetParts + DispatchContextAggregateMaxBytes - 1
	attemptID, err := schemawire.Max(defs["id"], "id", opt)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := 1 + len(`"attempt_id"`) + 1 + attemptID + 1 + len(`"worker_packet"`) + 1 + 1
	composed += wrapper
	if composed > maxWorkflowActionPayloadBytes {
		t.Fatalf("the composed declared maxima need %d bytes, above the %d-byte whole-action budget; derive the budget again", composed, maxWorkflowActionPayloadBytes)
	}
	if schemawire.NextPowerOfTwo(composed) != maxWorkflowActionPayloadBytes {
		t.Fatalf("composed declared maxima = %d bytes, whose next power of two is %d; the whole-action budget constant is %d and must stay the derivation's own rounding", composed, schemawire.NextPowerOfTwo(composed), maxWorkflowActionPayloadBytes)
	}
	// The research-context packet3 identity (CON-911) rides the same
	// composition: its work-context closure — the research entries and the
	// widened group namespace — sits inside the excluded work_context member,
	// so the packet3 derivation must match the packet2 derivation byte for
	// byte and the budget constant stays unchanged. No second budget and no
	// limit growth.
	packet3Opt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet3/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": OracleCombinedMaxBytes,
			"worker_packet3/inputs/design_record/workflow_design_record/recorded_at":      32,
			"worker_packet3/inputs/proposal_record/workflow_proposal_record/recorded_at":  32,
		},
		Excludes: map[string]bool{
			"worker_packet3/inputs/work_context": true,
			"worker_packet3/inputs/checkpoint":   true,
			"worker_packet3/inputs/correction":   true,
		},
		FindingRefMax: findingRefMax,
	}
	packet3Parts, err := schemawire.Max(defs["worker_packet3"], "worker_packet3", packet3Opt)
	if err != nil {
		t.Fatalf("the packet3 derivation hit an unbudgeted member: %v", err)
	}
	if packet3Parts != packetParts {
		t.Fatalf("the packet3 derivation = %d bytes, want the packet2 derivation %d; the research closure rides the unchanged context aggregate", packet3Parts, packetParts)
	}
	packet3Composed := packet3Parts + DispatchContextAggregateMaxBytes - 1 + wrapper
	if packet3Composed != composed {
		t.Fatalf("the packet3 composition = %d bytes, want the packet2 composition %d", packet3Composed, composed)
	}
	if maxWorkflowPredicateBytes != 64*1024 {
		t.Fatalf("predicate bound = %d, want the unchanged 65536", maxWorkflowPredicateBytes)
	}
}

// The whole-action decode budget refuses whole with its typed kind: an
// over-bound action payload decodes to KindInvalidPayload and nothing else.
func TestWorkflowActionPayloadBudgetRefusesTypedAndWhole(t *testing.T) {
	oversize := bytes.Repeat([]byte("x"), maxWorkflowActionPayloadBytes+1)
	var target map[string]any
	err := decodeWorkflowActionPayloadStrict(oversize, &target)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload {
		t.Fatalf("over-bound action payload error = %v, want %s refusing the whole decode", err, KindInvalidPayload)
	}
	if len(strings.TrimSpace(string(oversize))) != maxWorkflowActionPayloadBytes+1 {
		t.Fatal("the refusal must keep the whole request; nothing may trim it")
	}
}
