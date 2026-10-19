package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func findingInventory(analysis writerAnalysis) inventory {
	return inventory{
		Rows: []row{{Tag: "v1.0.0", Group: "g", Status: "inventoried"}},
		Groups: map[string][]registration{"g": {
			{Kind: "thing.a", CurrentVersion: 2, MinSupported: 1, Type: "t"},
		}},
		Analyses: map[string]writerAnalysis{"g": analysis},
		Types:    map[string]json.RawMessage{"t": json.RawMessage(`{"kind":"object","fields":[]}`)},
	}
}

// Resolved writes are reported beside the held sites, and hold nothing less.
func TestEveryOpenSiteIsHeld(t *testing.T) {
	analysis := writerAnalysis{Analyzer: "test", Writes: []string{"thing.a v2"}, Open: []writerSite{
		{Function: "store.readEvents", Expression: "&event.Kind", FunctionDigest: "sha256:5555", Pairs: []string{"* v*"}},
		{Function: "store.upcast", Expression: "event.PayloadVersion = 2", FunctionDigest: "sha256:1111", Pairs: []string{"* v2"}},
	}}
	findings := writerFindings(findingInventory(analysis))
	if len(findings) != 1 || findings[0].Refused != "" || !reflect.DeepEqual(findings[0].Held, analysis.Open) || !reflect.DeepEqual(findings[0].Writes, analysis.Writes) {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestRefusedOrMissingAnalysisIsRecorded(t *testing.T) {
	inv := findingInventory(writerAnalysis{Analyzer: "test", Refused: "type-check failed"})
	if f := writerFindings(inv); f[0].Refused != "type-check failed" {
		t.Fatalf("refused analysis: %+v", f[0])
	}
	delete(inv.Analyses, "g")
	if f := writerFindings(inv); f[0].Refused == "" {
		t.Fatalf("missing analysis: %+v", f[0])
	}
}
