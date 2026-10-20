package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// dischargeInventory has three inventoried releases: v1.0.0 holds every pair
// through one unreviewed wildcard site, v1.1.0 discharges every pair through
// one exempted read-back site, and v1.2.0 has no analysis at all. Groups g
// and h register thing.a accepting v1 below its current v2; group r registers
// thing.b the same way.
func dischargeInventory() inventory {
	held := findingAnalysis()
	held.Open[1].Pairs = []string{"* v*"}
	discharging := findingAnalysis()
	discharging.Open = discharging.Open[:1]
	discharging.Writes = nil
	return inventory{
		Rows: []row{
			{Tag: "v1.0.0", Group: "g", Status: "inventoried"},
			{Tag: "v1.1.0", Group: "h", Status: "inventoried"},
			{Tag: "v1.2.0", Group: "r", Status: "inventoried"},
		},
		Groups: map[string][]registration{
			"g": {{Kind: "thing.a", CurrentVersion: 2, MinSupported: 1, Type: "t"}},
			"h": {{Kind: "thing.a", CurrentVersion: 2, MinSupported: 1, Type: "t"}},
			"r": {{Kind: "thing.b", CurrentVersion: 2, MinSupported: 1, Type: "t"}},
		},
		Analyses: map[string]writerAnalysis{"g": held, "h": discharging},
		Types:    map[string]json.RawMessage{"t": json.RawMessage(`{"kind":"object","fields":[]}`)},
	}
}

// dischargeWitness is one writer-produced row: v1.0.0's own writer emitting
// thing.a v1, the older accepted version.
func dischargeWitness(t *testing.T) witness {
	t.Helper()
	const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	w := witness{
		Seq:            7,
		EventID:        "event-1",
		Kind:           "thing.a",
		PayloadVersion: 1,
		Payload:        `{}`,
		Writer: &writerSource{
			Tag:            "v1.0.0",
			AssetDigest:    "sha256:" + hex64,
			Recipe:         "gate",
			RecipeSHA256:   hex64,
			SnapshotSHA256: hex64,
		},
	}
	if !w.complete() {
		t.Fatalf("witness incomplete: %+v", w)
	}
	return w
}

// An older version is covered exactly when every release that accepts it is
// witnessed or discharged: missing_writers must not list a discharged
// release, a refusal must stay uncovered, and a shape stays uncovered unless
// a witness conforms to it or every writer discharged.
func TestOlderCoverageCombinesWitnessOrDischarge(t *testing.T) {
	report, err := cover(dischargeInventory(), []witness{dischargeWitness(t)}, findingTable(t))
	if err != nil {
		t.Fatal(err)
	}
	var mixed, refused *olderCoverage
	for i := range report.Older {
		switch report.Older[i].Kind {
		case "thing.a":
			mixed = &report.Older[i]
		case "thing.b":
			refused = &report.Older[i]
		}
	}
	if mixed == nil {
		t.Fatalf("no thing.a older entry: %+v", report.Older)
	}
	if !slices.Equal(mixed.Writers, []string{"v1.0.0", "v1.1.0"}) {
		t.Fatalf("writers %v", mixed.Writers)
	}
	if !slices.Equal(mixed.Witnesses, []string{"v1.0.0"}) || !slices.Equal(mixed.Discharged, []string{"v1.1.0"}) {
		t.Fatalf("witnesses %v discharged %v", mixed.Witnesses, mixed.Discharged)
	}
	if mixed.Missing != nil || !mixed.Covered {
		t.Fatalf("a witnessed and a discharged writer cover the version: %+v", mixed)
	}
	if refused == nil || !slices.Equal(refused.Missing, []string{"v1.2.0"}) || refused.Covered {
		t.Fatalf("a refused analysis must stay missing: %+v", refused)
	}
	var shape *shapeCoverage
	for i := range report.Shapes {
		if report.Shapes[i].Kind == "thing.a" {
			shape = &report.Shapes[i]
		}
	}
	if shape == nil || !slices.Equal(shape.Discharged, []string{"v1.1.0"}) || shape.Covered {
		t.Fatalf("the shape stays uncovered while v2 is undischarged at v1.0.0: %+v", shape)
	}
	if !reflect.DeepEqual(report.Unbound, []string{}) {
		t.Fatalf("unbound %v", report.Unbound)
	}
}
