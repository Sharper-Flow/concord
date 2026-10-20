package main

import (
	"encoding/json"
	"reflect"
	"slices"
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

// A replay-decoder site is exempted under its own class and holds the kinds
// whose folds admit a work_item subject, or every kind once checkSubject
// drifts. A site the entry does not cover holds nothing by kind.
func TestReplayDecoderHoldsWorkItemKinds(t *testing.T) {
	data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{
		{ID: "replay", Class: "replay_decoder", Function: "store.upcast", Expression: "event.PayloadVersion = 2", FunctionDigest: "sha256:1111", CheckSubjectDigest: "sha256:2222", Check: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := parseExemptions(data)
	if err != nil {
		t.Fatal(err)
	}
	analysis := findingAnalysis()
	analysis.WorkItemFolds, analysis.CheckSubject = []string{"thing.a"}, "sha256:2222"
	f := finding(t, analysis, table)
	if !reflect.DeepEqual(f.Exempted, []exemptedSite{{analysis.Open[1], "replay", "replay_decoder"}}) || !reflect.DeepEqual(f.ReplayHeld, []string{"thing.a"}) {
		t.Fatalf("exempted %+v replay held %v", f.Exempted, f.ReplayHeld)
	}
	analysis.CheckSubject = "sha256:3333"
	if f := finding(t, analysis, table); !reflect.DeepEqual(f.ReplayHeld, []string{"*"}) {
		t.Fatalf("drifted checkSubject: replay held %v", f.ReplayHeld)
	}
	if f := finding(t, analysis, findingTable(t)); f.ReplayHeld != nil {
		t.Fatalf("uncovered decoder: replay held %v", f.ReplayHeld)
	}
}

// A confined upcaster site is exempted while the reviewed dispatcher and
// guard hold, and its finding names the current version of every kind with
// an older supported version or an upcaster; any drift leaves it held.
func TestUpcastConfinementHoldsCurrentVersions(t *testing.T) {
	data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{
		{ID: "upcast", Class: "upcast_confinement", Dispatcher: "store.dispatch", DispatcherDigest: "sha256:2222", Guard: "event.PayloadVersion != registration.CurrentVersion", Callers: []namedHash{{"store.prepare", "sha256:3333"}}, Check: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := parseExemptions(data)
	if err != nil {
		t.Fatal(err)
	}
	analysis := func() writerAnalysis {
		a := findingAnalysis()
		a.Open = slices.Clone(a.Open)
		a.Open[1].Confinement = "upcaster"
		a.Dispatcher = &upcastDispatcher{Function: "store.dispatch", FunctionDigest: "sha256:2222", Guards: []string{"upcaster == nil", "event.PayloadVersion != registration.CurrentVersion"}, Callers: []namedHash{{"store.prepare", "sha256:3333"}}}
		return a
	}
	inv := findingInventory(analysis())
	inv.Groups["g"] = append(inv.Groups["g"], registration{Kind: "thing.b", CurrentVersion: 1, MinSupported: 1, Type: "t"}, registration{Kind: "thing.c", CurrentVersion: 3, MinSupported: 3, Upcasters: []int{2}, RecordedSchemas: []int{3}, Type: "t"})
	f := writerFindings(inv, table)[0]
	if !reflect.DeepEqual(f.Exempted, []exemptedSite{{inv.Analyses["g"].Open[1], "upcast", "upcast_confinement"}}) || !reflect.DeepEqual(f.UpcastHeld, []string{"thing.a v2", "thing.c v3"}) {
		t.Fatalf("exempted %+v upcast held %v", f.Exempted, f.UpcastHeld)
	}
	for name, drift := range map[string]func(*writerAnalysis){
		"dispatcher changed": func(a *writerAnalysis) { a.Dispatcher.FunctionDigest = "sha256:9999" },
		"dispatcher renamed": func(a *writerAnalysis) { a.Dispatcher.Function = "store.other" },
		"guard dropped":      func(a *writerAnalysis) { a.Dispatcher.Guards = a.Dispatcher.Guards[:1] },
		"caller changed":     func(a *writerAnalysis) { a.Dispatcher.Callers = []namedHash{{"store.prepare", "sha256:9999"}} },
		"caller added": func(a *writerAnalysis) {
			a.Dispatcher.Callers = append(a.Dispatcher.Callers, namedHash{"store.other", "sha256:4444"})
		},
		"adapt appeared": func(a *writerAnalysis) {
			a.Adapt = &adaptRoute{Function: "store.adaptCaller", FunctionDigest: "sha256:8888"}
		},
		"no dispatcher":    func(a *writerAnalysis) { a.Dispatcher = nil },
		"confinement lost": func(a *writerAnalysis) { a.Open[1].Confinement = "" },
	} {
		a := analysis()
		drift(&a)
		if f := finding(t, a, table); len(f.Exempted) != 0 || f.UpcastHeld != nil || len(f.Held) != 2 {
			t.Errorf("%s: held %+v exempted %+v upcast held %v", name, f.Held, f.Exempted, f.UpcastHeld)
		}
	}
}

// An adapt-keyed entry matches only the proven route: the same release
// exempts through it, and any drift of the adapt caller or forwarders leaves
// the site held.
func TestAdaptKeyedUpcastEntryMatchesOnlyTheProvenRoute(t *testing.T) {
	analysis := func() writerAnalysis {
		a := findingAnalysis()
		a.Open = slices.Clone(a.Open)
		a.Open[1].Confinement = "upcaster"
		a.Dispatcher = &upcastDispatcher{Function: "store.dispatch", FunctionDigest: "sha256:2222", Guards: []string{"event.PayloadVersion != registration.CurrentVersion"}, Callers: []namedHash{{"store.prepare", "sha256:3333"}}}
		a.Adapt = &adaptRoute{Function: "store.adaptCaller", FunctionDigest: "sha256:8888", Forwarders: []namedHash{{"store.forward", "sha256:7777"}}, Values: []namedHash{{"store.adaptValue", "sha256:5555"}}}
		return a
	}
	entry := func(adapt bool) exemptionTable {
		e := exemption{ID: "upcast", Class: "upcast_confinement", Dispatcher: "store.dispatch", DispatcherDigest: "sha256:2222",
			Guard: "event.PayloadVersion != registration.CurrentVersion", Callers: []namedHash{{"store.prepare", "sha256:3333"}}, Check: "test"}
		if adapt {
			e.ID, e.AdaptCaller, e.AdaptCallerDigest = "upcast:adapt", "store.adaptCaller", "sha256:8888"
			e.Forwarders = []namedHash{{"store.forward", "sha256:7777"}}
			e.AdaptValues = []namedHash{{"store.adaptValue", "sha256:5555"}}
		}
		data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{e}})
		if err != nil {
			t.Fatal(err)
		}
		table, err := parseExemptions(data)
		if err != nil {
			t.Fatal(err)
		}
		return table
	}
	if f := finding(t, analysis(), entry(true)); len(f.Exempted) != 1 {
		t.Fatalf("adapt entry must cover the proven route: %+v", f)
	}
	for name, drift := range map[string]func(*writerAnalysis){
		"route gone":        func(a *writerAnalysis) { a.Adapt = nil },
		"caller drifted":    func(a *writerAnalysis) { a.Adapt.FunctionDigest = "sha256:9999" },
		"forwarder changed": func(a *writerAnalysis) { a.Adapt.Forwarders = []namedHash{{"store.forward", "sha256:9999"}} },
		"forwarder added": func(a *writerAnalysis) {
			a.Adapt.Forwarders = append(a.Adapt.Forwarders, namedHash{"store.other", "sha256:6666"})
		},
		"adapt value drifted": func(a *writerAnalysis) {
			a.Adapt.Values = []namedHash{{"store.adaptValue", "sha256:9999"}}
		},
		"adapt value added": func(a *writerAnalysis) {
			a.Adapt.Values = append(a.Adapt.Values, namedHash{"store.otherValue", "sha256:4444"})
		},
	} {
		a := analysis()
		drift(&a)
		if f := finding(t, a, entry(true)); len(f.Exempted) != 0 || len(f.Held) != 2 {
			t.Errorf("%s: held %+v exempted %+v", name, f.Held, f.Exempted)
		}
	}
	if f := finding(t, analysis(), entry(false)); len(f.Exempted) != 0 || len(f.Held) != 2 {
		t.Fatalf("a route-free entry must not cover a release with an adapt route: %+v", f)
	}
}

// A discharge is the exemption table's proof that a registered pair has no
// writer at a release: exempted sites discharge the pairs they cover, a held
// site or an upcast_held entry blocks its pairs, and a kind no site or write
// names at all discharges on no evidence.
func TestDischargeProvesUnwritablePairs(t *testing.T) {
	analysis := findingAnalysis()
	f := finding(t, analysis, findingTable(t))
	if !reflect.DeepEqual(f.Discharged, []discharge{{Kind: "thing.a", PayloadVersion: 1, Exemptions: []string{"read"}}}) {
		t.Fatalf("discharged %+v", f.Discharged)
	}
	if !reflect.DeepEqual(f.Undischarged, []string{"thing.a v2"}) {
		t.Fatalf("undischarged %v", f.Undischarged)
	}
	// The held upcast site, once exempted, discharges nothing its own
	// confinement holds: the current version stays an obligation and the
	// older one discharges on the read exemption alone, because the
	// confined site's own pairs name only the current version.
	data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{
		{ID: "read", Class: "read_back", Function: "store.readEvents", Expression: "&event.Kind", FunctionDigest: readDigest, Check: "test"},
		{ID: "upcast", Class: "upcast_confinement", Dispatcher: "store.dispatch", DispatcherDigest: "sha256:2222", Guard: "event.PayloadVersion != registration.CurrentVersion", Callers: []namedHash{{"store.prepare", "sha256:3333"}}, Check: "test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := parseExemptions(data)
	if err != nil {
		t.Fatal(err)
	}
	upcast := findingAnalysis()
	upcast.Open[1].Confinement = "upcaster"
	upcast.Dispatcher = &upcastDispatcher{Function: "store.dispatch", FunctionDigest: "sha256:2222", Guards: []string{"event.PayloadVersion != registration.CurrentVersion"}, Callers: []namedHash{{"store.prepare", "sha256:3333"}}}
	f = finding(t, upcast, table)
	if !reflect.DeepEqual(f.Discharged, []discharge{{Kind: "thing.a", PayloadVersion: 1, Exemptions: []string{"read"}}}) {
		t.Fatalf("discharged %+v", f.Discharged)
	}
	if !reflect.DeepEqual(f.Undischarged, []string{"thing.a v2"}) {
		t.Fatalf("undischarged %v", f.Undischarged)
	}
	// A kind no write or held site names discharges through the pairs its
	// exemptions cover, and cover counts it covered exactly when every
	// registering writer discharged.
	inv := findingInventory(analysis)
	inv.Groups["g"] = append(inv.Groups["g"], registration{Kind: "thing.b", CurrentVersion: 1, MinSupported: 1, Type: "t"})
	findings := writerFindings(inv, findingTable(t))
	if b := findings[0]; !slices.ContainsFunc(b.Discharged, func(d discharge) bool {
		return d.Kind == "thing.b" && d.PayloadVersion == 1 && len(d.Exemptions) == 1 && d.Exemptions[0] == "read"
	}) {
		t.Fatalf("thing.b not discharged on no evidence: %+v", b.Discharged)
	}
	report, err := cover(inv, nil, findingTable(t))
	if err != nil {
		t.Fatal(err)
	}
	var bShape, aShape *shapeCoverage
	for i := range report.Shapes {
		switch report.Shapes[i].Kind {
		case "thing.b":
			bShape = &report.Shapes[i]
		case "thing.a":
			aShape = &report.Shapes[i]
		}
	}
	if bShape == nil || !bShape.Covered || !reflect.DeepEqual(bShape.Discharged, []string{"v1.0.0"}) {
		t.Fatalf("thing.b shape %+v", bShape)
	}
	if aShape == nil || aShape.Covered {
		t.Fatalf("thing.a shape must stay uncovered while v2 is undischarged: %+v", aShape)
	}
}

// The registered-kind pin keys the registry-lookup helper the reviewed
// callers pass event.Kind through. An upcast entry covers a confined site
// only through the exact helper declaration: a drifted helper body, a
// renamed helper, a helperless analysis, or a pin-less entry refuses the
// cover, with every dispatcher, caller, and adapt key held fixed, and the
// confined pair stays undischarged.
func TestRegisteredKindPinRefusesHelperDrift(t *testing.T) {
	const good = "sha256:b09e1e161eb2d2e64d9249c7201e610f5d60966492a1bb098d7c7be000fec67e"
	const name = storePkg + ".registeredEventKind"
	analysis := func(helper *namedHash) writerAnalysis {
		a := findingAnalysis()
		a.Open = a.Open[1:2]
		a.Open[0].Confinement = "upcaster"
		a.Dispatcher = &upcastDispatcher{
			Function:       "store.dispatch",
			FunctionDigest: "sha256:2222",
			Guards:         []string{"event.PayloadVersion != registration.CurrentVersion"},
			Callers:        []namedHash{{"store.prepare", "sha256:3333"}},
			RegisteredKind: helper,
		}
		return a
	}
	table := func(pinnedName, pinnedDigest string) exemptionTable {
		e := exemption{ID: "upcast", Class: "upcast_confinement", Dispatcher: "store.dispatch", DispatcherDigest: "sha256:2222",
			Guard: "event.PayloadVersion != registration.CurrentVersion", Callers: []namedHash{{"store.prepare", "sha256:3333"}}, Check: "test"}
		if pinnedName != "" {
			e.RegisteredKind, e.RegisteredKindDigest = pinnedName, pinnedDigest
		}
		data, err := json.Marshal(exemptionTable{Schema: exemptionsSchema, Entries: []exemption{e}})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseExemptions(data)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	if f := finding(t, analysis(&namedHash{name, good}), table(name, good)); len(f.Exempted) != 1 {
		t.Fatalf("the exact helper must cover the confined site: %+v", f)
	}
	for name, drifted := range map[string]*namedHash{
		"helper body drifted": {storePkg + ".registeredEventKind", "sha256:9999"},
		"helper renamed":      {storePkg + ".registeredEventKindV2", good},
	} {
		f := finding(t, analysis(drifted), table(storePkg+".registeredEventKind", good))
		if len(f.Exempted) != 0 || len(f.Held) != 1 || slices.ContainsFunc(f.Discharged, func(d discharge) bool { return d.PayloadVersion == 2 }) {
			t.Errorf("%s: held %+v exempted %+v discharged %+v", name, f.Held, f.Exempted, f.Discharged)
		}
	}
	if f := finding(t, analysis(nil), table(storePkg+".registeredEventKind", good)); len(f.Exempted) != 0 || len(f.Held) != 1 {
		t.Fatalf("helperless analysis: held %+v exempted %+v", f.Held, f.Exempted)
	}
	if f := finding(t, analysis(&namedHash{storePkg + ".registeredEventKind", good}), table("", "")); len(f.Exempted) != 0 || len(f.Held) != 1 {
		t.Fatalf("unpinned entry over a pinned analysis: held %+v exempted %+v", f.Held, f.Exempted)
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
	// A refusal fails closed through the discharge join: neither finding
	// may discharge any pair of its release.
	for name, f := range map[string]writerFinding{
		"refused": finding(t, refused, findingTable(t)),
		"missing": writerFindings(inv, findingTable(t))[0],
	} {
		if len(f.Discharged) != 0 {
			t.Errorf("%s analysis discharged %v", name, f.Discharged)
		}
	}
}

// A registration whose accepted range is not a fact — a minimum below 1, or
// a current below its minimum — proves nothing unwritable: no version of it
// discharges, and the versions it names stay undischarged. The invalid
// registrations join the group's inventory here rather than in
// findingInventory, whose other tests assert exact lists.
func TestInvalidRegistrationDischargesNothing(t *testing.T) {
	inv := findingInventory(findingAnalysis())
	inv.Groups["g"] = append(inv.Groups["g"],
		registration{Kind: "thing.c", CurrentVersion: 1, MinSupported: 0, Type: "t"},
		registration{Kind: "thing.d", CurrentVersion: 1, MinSupported: 3, Type: "t"},
	)
	f := writerFindings(inv, findingTable(t))[0]
	if slices.ContainsFunc(f.Discharged, func(d discharge) bool { return d.Kind == "thing.c" || d.Kind == "thing.d" }) {
		t.Fatalf("an invalid registration discharged %+v", f.Discharged)
	}
	for _, pair := range []string{"thing.c v0", "thing.c v1"} {
		if !slices.Contains(f.Undischarged, pair) {
			t.Fatalf("%s not undischarged: %v", pair, f.Undischarged)
		}
	}
}

// A held site that names no pairs cannot prove any pair unwritable, so it
// must block every pair: a discharge must never rest on an open site whose
// emissions are unknown.
func TestEmptyHeldPairsFailClosed(t *testing.T) {
	analysis := findingAnalysis()
	analysis.Writes = nil
	analysis.Open[1].Pairs = nil
	f := finding(t, analysis, findingTable(t))
	if len(f.Held) != 1 || len(f.Held[0].Pairs) != 0 || len(f.Discharged) != 0 {
		t.Fatalf("held %+v discharged %+v", f.Held, f.Discharged)
	}
	if !reflect.DeepEqual(f.Undischarged, []string{"thing.a v1", "thing.a v2"}) {
		t.Fatalf("undischarged %v", f.Undischarged)
	}
}

// A pair form the matcher cannot resolve matches every version, so an
// unknown kind, an unregistered cur: kind, and a non-numeric version all
// stay blocked rather than discharge on a guess.
func TestPairMatchesFailsClosedOnUnknownForms(t *testing.T) {
	current := map[string]int{"thing": 2}
	for name, tc := range map[string]struct {
		pair    string
		kind    string
		version int
		want    bool
	}{
		"exact match":                {"thing v2", "thing", 2, true},
		"exact miss":                 {"thing v2", "thing", 1, false},
		"other kind":                 {"thing v2", "other", 2, false},
		"wildcard kind":              {"* v2", "thing", 2, true},
		"wildcard kind version miss": {"* v2", "thing", 3, false},
		"wildcard version":           {"thing v*", "thing", 1, true},
		"no version":                 {"thing", "thing", 1, true},
		"current match":              {"thing vcur:thing", "thing", 2, true},
		"current miss":               {"thing vcur:thing", "thing", 1, false},
		"unregistered cur kind":      {"thing vcur:other", "thing", 5, true},
		"non-numeric version":        {"thing vtwo", "thing", 2, true},
	} {
		if got := pairMatches(tc.pair, tc.kind, tc.version, current); got != tc.want {
			t.Errorf("%s: pairMatches(%q, %q, %d) = %v, want %v", name, tc.pair, tc.kind, tc.version, got, tc.want)
		}
	}
}

func TestExemptionTableRefusesMalformedEntries(t *testing.T) {
	const entry = `{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c"}`
	for name, data := range map[string]string{
		"unknown class":                   `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"other","function":"f","expression":"e","function_digest":"d","check":"c"}]}`,
		"missing key":                     `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","check":"c"}]}`,
		"missing check":                   `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d"}]}`,
		"missing id":                      `{"schema":"concord.release-writer-exemptions.v1","entries":[{"class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c"}]}`,
		"repeated id":                     `{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `,` + entry + `]}`,
		"unknown field":                   `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"x","class":"read_back","function":"f","expression":"e","function_digest":"d","check":"c","note":"n"}]}`,
		"wrong schema":                    `{"schema":"other","entries":[]}`,
		"data after":                      `{"schema":"concord.release-writer-exemptions.v1","entries":[]} {}`,
		"closing after":                   `{"schema":"concord.release-writer-exemptions.v1","entries":[]}}`,
		"repeated key":                    `{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `,` + strings.Replace(entry, `"x"`, `"y"`, 1) + `]}`,
		"adapt caller without digest":     `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","adapt_caller":"c","check":"c"}]}`,
		"comparison without a comparator": `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"c","class":"comparison_only","function":"f","function_digest":"d","caller":"c","caller_digest":"h","check":"c"}]}`,
		"adapt values without a caller":   `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","adapt_values":[{"function":"c","digest":"h"}],"check":"c"}]}`,
		"route without adapt values":      `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","adapt_caller":"c","adapt_caller_digest":"h","forwarders":[{"function":"e","digest":"f"}],"check":"c"}]}`,
		"forwarders without a caller":     `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","forwarders":[{"function":"c","digest":"h"}],"check":"c"}]}`,
		"upcast without guard":            `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","check":"c"}]}`,
		"upcast with a site key":          `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","callers":[{"function":"c","digest":"h"}],"function":"f","check":"c"}]}`,
		"read-back with a dispatcher":     `{"schema":"concord.release-writer-exemptions.v1","entries":[` + strings.Replace(entry, `"check":"c"`, `"dispatcher":"d","check":"c"`, 1) + `]}`,
		"registered kind without digest":  `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","callers":[{"function":"c","digest":"h"}],"registered_kind":"h","check":"c"}]}`,
		"registered kind without a name":  `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","callers":[{"function":"c","digest":"h"}],"registered_kind_digest":"sha256:d","check":"c"}]}`,
		"read-back with registered kind":  `{"schema":"concord.release-writer-exemptions.v1","entries":[` + strings.Replace(entry, `"check":"c"`, `"registered_kind":"h","registered_kind_digest":"sha256:d","check":"c"`, 1) + `]}`,
		"repeated upcast key":             `{"schema":"concord.release-writer-exemptions.v1","entries":[{"id":"u","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","callers":[{"function":"c","digest":"h"}],"check":"c"},{"id":"v","class":"upcast_confinement","dispatcher":"d","dispatcher_digest":"g","guard":"x","callers":[{"function":"c","digest":"h"}],"check":"c"}]}`,
		"site key in two classes":         `{"schema":"concord.release-writer-exemptions.v1","entries":[` + entry + `,` + strings.TrimSuffix(strings.Replace(strings.Replace(entry, "read_back", "replay_decoder", 1), `"x"`, `"y"`, 1), "}") + `,"check_subject_digest":"s"}]}`,
		"replay without check subject":    `{"schema":"concord.release-writer-exemptions.v1","entries":[` + strings.Replace(entry, "read_back", "replay_decoder", 1) + `]}`,
		"read-back with check subject":    `{"schema":"concord.release-writer-exemptions.v1","entries":[` + strings.Replace(entry, `"check":"c"`, `"check_subject_digest":"s","check":"c"`, 1) + `]}`,
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
