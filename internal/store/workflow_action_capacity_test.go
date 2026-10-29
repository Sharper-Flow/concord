package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

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

// The shared repository-source schema leaves commit_oid to its consumer.
// Oracle capacity uses the combined writer bound; the recipe's own scalar
// guard must still refuse values outside the declared Git identity domain.
func TestOracleRecipeCommitOIDCapacityOwner(t *testing.T) {
	for _, length := range []int{39, 40, 64, 65} {
		source := WorkContextReadingSource{
			Kind: WorkContextSourceRepositoryFile, ProjectID: "project-fixture",
			Path: "scripts/oracle.json", CommitOID: strings.Repeat("a", length),
		}
		if err := validateOracleRecipeSource(source); (err == nil) != (length >= 40 && length <= 64) {
			t.Fatalf("recipe commit length %d: %v", length, err)
		}
	}
}

func TestWorkflowCurrentActionWriterBounds(t *testing.T) {
	if WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", WorkContextActionFindingsMax)
	}
	if maxWorkflowPredicateBytes != 64*1024 {
		t.Fatalf("predicate bound = %d, want the unchanged 65536", maxWorkflowPredicateBytes)
	}
}

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
