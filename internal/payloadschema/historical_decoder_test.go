package payloadschema

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// historicalDecoderCorpus pairs one frozen historical schema ref with one
// payload the schema accepts and one it refuses. Both sides of the corpus
// must verdict identically through the frozen historical decoder and the
// public authoring document while the two documents carry the same bytes.
var historicalDecoderCorpus = []struct {
	name    string
	valid   string
	invalid string
}{
	{
		name:    "workflow_design_decision",
		valid:   `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}`,
		invalid: `{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}`,
	},
	{
		name:    "workflow_forward_relation",
		valid:   `{"kind":"forward_link"}`,
		invalid: `{"kind":"blocks"}`,
	},
	{
		name:    "native_report_timestamp",
		valid:   `"2026-10-10T12:00:00Z"`,
		invalid: `"not-a-time"`,
	},
}

// collectDefs returns the document's $defs member names in sorted order.
func collectDefs(t *testing.T, document map[string]any) []string {
	t.Helper()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema document carries no $defs object")
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestHistoricalDecoderDocumentIsFrozenAndClosed proves the frozen historical
// decoder document parses, carries its own $defs, and resolves every internal
// reference without consulting the public authoring document.
func TestHistoricalDecoderDocumentIsFrozenAndClosed(t *testing.T) {
	document := HistoricalDocument()
	defs := collectDefs(t, document)
	if len(defs) == 0 {
		t.Fatal("the frozen historical decoder document is empty")
	}
	publicNames := collectDefs(t, Document())
	for _, ref := range historicalDecoderRefs(t) {
		if !HasIn(document, ref) {
			t.Errorf("frozen historical decoder does not declare schema %q", ref)
		}
		if !slices.Contains(publicNames, ref) {
			t.Errorf("frozen historical decoder carries %q, which no baseline definition declared", ref)
		}
	}
	// Every $defs/$ref inside the frozen document resolves within the frozen
	// document itself: the decoder is closed and needs no public member.
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if !HasIn(document, name) {
					t.Errorf("frozen historical decoder carries a dangling internal reference %q", ref)
				}
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, entry := range typed {
				walk(entry)
			}
		}
	}
	defsObject := document["$defs"].(map[string]any)
	for _, name := range defs {
		walk(defsObject[name])
	}
}

// TestHistoricalDecoderValidatesIndependentlyOfPublicAuthoringDocument is the
// S0 red/green witness: a public-only schema change (a definition removed from
// the public authoring document) must not break historical payload
// validation, because the frozen decoder resolves the ref itself. Baseline
// authoring stays unchanged: the full public document still validates current
// payloads, and both documents refuse the same invalid corpus.
func TestHistoricalDecoderValidatesIndependentlyOfPublicAuthoringDocument(t *testing.T) {
	publicOnly := deepCopyDocument(t, Document())
	publicDefs := publicOnly["$defs"].(map[string]any)
	for _, ref := range historicalDecoderRefs(t) {
		delete(publicDefs, ref)
	}
	for _, entry := range historicalDecoderCorpus {
		if err := ValidateInDocument(publicOnly, entry.name, []byte(entry.valid)); err == nil {
			t.Errorf("public-only copy still resolved %q after the simulated public-only schema change", entry.name)
		}
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.valid)); err != nil {
			t.Errorf("frozen historical decoder refused a valid historical payload for %q: %v", entry.name, err)
		}
		if err := ValidateInDocument(HistoricalDocument(), entry.name, []byte(entry.invalid)); err == nil {
			t.Errorf("frozen historical decoder accepted an invalid historical payload for %q", entry.name)
		}
		if err := Validate(entry.name, []byte(entry.valid)); err != nil {
			t.Errorf("baseline public authoring document refused the same valid payload for %q: %v", entry.name, err)
		}
		if err := Validate(entry.name, []byte(entry.invalid)); err == nil {
			t.Errorf("baseline public authoring document accepted the same invalid payload for %q", entry.name)
		}
	}
}

// TestHistoricalDecoderMatchesPublicAuthoringDocumentAtBaseline freezes the
// S0 baseline: every frozen decoder definition is byte-equal to the public
// authoring document's definition of the same name, so both sides accept and
// refuse the identical baseline corpus. A later slice that narrows the public
// authoring document removes or replaces this assertion with its own
// conscious divergence evidence; the frozen decoder itself never re-derives.
func TestHistoricalDecoderMatchesPublicAuthoringDocumentAtBaseline(t *testing.T) {
	frozen := HistoricalDocument()
	public := Document()
	frozenDefs := frozen["$defs"].(map[string]any)
	publicDefs := public["$defs"].(map[string]any)
	for name, frozenDef := range frozenDefs {
		publicDef, ok := publicDefs[name]
		if !ok {
			t.Errorf("frozen historical decoder definition %q has no baseline public counterpart", name)
			continue
		}
		if canonicalJSON(t, frozenDef) != canonicalJSON(t, publicDef) {
			t.Errorf("frozen historical decoder definition %q diverged from the baseline public authoring document", name)
		}
	}
}

// historicalDecoderRefs returns the declared schema refs the frozen decoder
// must carry: one from the corpus plus a spot check of the closure.
func historicalDecoderRefs(t *testing.T) []string {
	t.Helper()
	names := make([]string, 0, len(historicalDecoderCorpus)+1)
	for _, entry := range historicalDecoderCorpus {
		names = append(names, entry.name)
	}
	names = append(names, "reference")
	return names
}

func deepCopyDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var replica map[string]any
	if err := json.Unmarshal(raw, &replica); err != nil {
		t.Fatal(err)
	}
	return replica
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
