package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const readDigest = "sha256:5555"

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

// findingAnalysis has one read-back site and one other open site.
func findingAnalysis() writerAnalysis {
	return writerAnalysis{Analyzer: "test", Writes: []string{"thing.a v2"}, Open: []writerSite{
		{Function: "store.readEvents", Expression: "&event.Kind", FunctionDigest: readDigest, Pairs: []string{"* v*"}},
		{Function: "store.upcast", Expression: "event.PayloadVersion = 2", FunctionDigest: "sha256:1111", Pairs: []string{"* v2"}},
	}}
}

func findingTable(t *testing.T) exemptionTable {
	t.Helper()
	data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{
		{ID: "read", Class: "read_back", Function: "store.readEvents", Expression: "&event.Kind", FunctionDigest: readDigest, Check: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := parseExemptions(data)
	if err != nil || table.digest != digest(data) {
		t.Fatalf("table digest %q: %v", table.digest, err)
	}
	return table
}

func finding(t *testing.T, analysis writerAnalysis, table exemptionTable) writerFinding {
	t.Helper()
	findings := writerFindings(findingInventory(analysis), table)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	return findings[0]
}

// A covered site is reported as exempted, every other open site stays held,
// and resolved writes are reported beside them.
func TestExemptedSitesAreReportedApartFromHeldSites(t *testing.T) {
	analysis := findingAnalysis()
	table := findingTable(t)
	f := finding(t, analysis, table)
	if f.Refused != "" || f.Exemptions != table.digest || !reflect.DeepEqual(f.Writes, analysis.Writes) {
		t.Fatalf("finding = %+v", f)
	}
	if !reflect.DeepEqual(f.Held, analysis.Open[1:]) || !reflect.DeepEqual(f.Exempted, []exemptedSite{{analysis.Open[0], "read", "read_back"}}) {
		t.Fatalf("held %+v exempted %+v", f.Held, f.Exempted)
	}
}

// A replay-decoder site is exempted under its own class.
func TestReplayDecoderSiteKeepsItsClass(t *testing.T) {
	analysis := findingAnalysis()
	data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{
		{ID: "replay", Class: "replay_decoder", Function: "store.upcast", Expression: "event.PayloadVersion = 2", FunctionDigest: "sha256:1111", Check: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := parseExemptions(data)
	if err != nil {
		t.Fatal(err)
	}
	if f := finding(t, analysis, table); !reflect.DeepEqual(f.Exempted, []exemptedSite{{analysis.Open[1], "replay", "replay_decoder"}}) {
		t.Fatalf("exempted %+v", f.Exempted)
	}
}

func TestDriftLeavesTheSiteHeld(t *testing.T) {
	for name, edit := range map[string]func(*writerSite){
		"function digest changed": func(s *writerSite) { s.FunctionDigest = "sha256:9999" },
		"expression changed":      func(s *writerSite) { s.Expression = "&event.PayloadVersion" },
		"function changed":        func(s *writerSite) { s.Function = "store.readOther" },
	} {
		t.Run(name, func(t *testing.T) {
			analysis := findingAnalysis()
			edit(&analysis.Open[0])
			if f := finding(t, analysis, findingTable(t)); len(f.Exempted) != 0 || !reflect.DeepEqual(f.Held, analysis.Open) {
				t.Fatalf("held %+v exempted %+v", f.Held, f.Exempted)
			}
		})
	}
}

func TestRefusedOrMissingAnalysisIsRecorded(t *testing.T) {
	refused := findingAnalysis()
	refused.Refused = "type-check failed"
	if f := finding(t, refused, findingTable(t)); f.Refused != "type-check failed" || f.Held != nil || f.Exempted != nil {
		t.Fatalf("refused analysis: %+v", f)
	}
	inv := findingInventory(findingAnalysis())
	delete(inv.Analyses, "g")
	if f := writerFindings(inv, findingTable(t)); f[0].Refused == "" || f[0].Held != nil || f[0].Exempted != nil {
		t.Fatalf("missing analysis: %+v", f[0])
	}
}

func TestExemptionTableRefusesMalformedEntries(t *testing.T) {
	const entry = `{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c"}`
	for name, data := range map[string]string{
		"unknown class": `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"other","function":"f","expression":"e","function_digest":"d","check":"c"}]}`,
		"missing key":   `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","check":"c"}]}`,
		"missing check": `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d"}]}`,
		"missing id":    `{"schema":"concord.release-writer-exemptions.v1","entries":[{"class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c"}]}`,
		"repeated id":   `{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `,` + entry + `]}`,
		"unknown field": `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c","note":"n"}]}`,
		"wrong schema":  `{"schema":"other","entries":[]}`,
		"data after":    `{"schema":"concord.release-writer-exemptions.v1","entries":[]} {}`,
		"closing after": `{"schema":"concord.release-writer-exemptions.v1","entries":[]}}`,
		"repeated key":  `{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `,` + strings.Replace(entry, `"x"`, `"y"`, 1) + `]}`,
	} {
		if _, err := parseExemptions([]byte(data)); err == nil {
			t.Errorf("%s: table admitted", name)
		}
	}
	if _, err := parseExemptions([]byte(`{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `]}`)); err != nil {
		t.Fatalf("a valid table: %v", err)
	}
	if _, err := parseExemptions(exemptionsSource); err != nil {
		t.Fatalf("the committed table: %v", err)
	}
}
