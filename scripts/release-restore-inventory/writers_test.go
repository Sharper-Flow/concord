package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixture is a synthetic module shaped like a release: a registry with an
// upcaster chain and a recorded-schema adapt, one dispatcher, a plain insert,
// a typed writer, a read-back scan, and a cmd reconstruction.
const fixtureStore = `package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type SubjectType string

const (
	SubjectWorkItem SubjectType = "work_item"
	SubjectProduct  SubjectType = "product"
)

type Event struct {
	Kind           string
	SubjectType    SubjectType
	PayloadVersion int
	Payload        []byte
}

type Tx struct{}
type State struct{}

type Upcaster func(Event) (Event, error)

type recordedPayloadSchema struct {
	matches func(map[string]json.RawMessage) bool
	adapt   func(Event) (Event, error)
}

type EventKindRegistration struct {
	CurrentVersion  int
	MinSupported    int
	Upcasters       map[int]Upcaster
	RecordedSchemas map[int][]recordedPayloadSchema
	Fold            func(*Tx, *State, Event) error
}

var eventKindRegistry = map[string]EventKindRegistration{
	"thing.a": {CurrentVersion: 2, MinSupported: 1, Upcasters: map[int]Upcaster{1: upcastA1}, Fold: foldA},
	"thing.b": {CurrentVersion: 3, MinSupported: 1, Upcasters: map[int]Upcaster{1: upcastB1, 2: upcastB2},
		RecordedSchemas: map[int][]recordedPayloadSchema{2: {{matches: matchB, adapt: upcastB2}}}, Fold: foldB},
}

func registeredEventKind(kind string) (EventKindRegistration, bool) {
	registration, ok := eventKindRegistry[kind]
	return registration, ok
}

func checkSubject(event Event, want SubjectType) error {
	if event.SubjectType != want {
		return errors.New("subject")
	}
	return nil
}

func foldA(tx *Tx, state *State, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	return nil
}

func foldB(tx *Tx, state *State, event Event) error { return nil }

func upcastA1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}

func upcastB1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}

func upcastB2(event Event) (Event, error) {
	event.PayloadVersion = 3
	return event, nil
}

func matchB(payload map[string]json.RawMessage) bool { return len(payload) > 0 }

func validateRegistry() error {
	for _, registration := range eventKindRegistry {
		for version := registration.MinSupported; version < registration.CurrentVersion; version++ {
			if registration.Upcasters[version] == nil {
				return errors.New("no upcaster")
			}
		}
		for _, schemas := range registration.RecordedSchemas {
			for _, schema := range schemas {
				if schema.matches == nil || schema.adapt == nil {
					return errors.New("no adapt")
				}
			}
		}
	}
	return nil
}

func upcastEventWithRegistration(event Event, registration EventKindRegistration) (Event, error) {
	if event.PayloadVersion < registration.MinSupported || event.PayloadVersion > registration.CurrentVersion {
		return Event{}, errors.New("unsupported")
	}
	for event.PayloadVersion < registration.CurrentVersion {
		upcaster := registration.Upcasters[event.PayloadVersion]
		if upcaster == nil {
			return Event{}, errors.New("no upcaster")
		}
		var err error
		event, err = upcaster(event)
		if err != nil {
			return Event{}, err
		}
	}
	return event, nil
}

func prepareRecordedEvent(event Event) (Event, error) {
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return Event{}, errors.New("unknown")
	}
	if schemas := registration.RecordedSchemas[event.PayloadVersion]; len(schemas) != 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Event{}, err
		}
		selected := -1
		for i, schema := range schemas {
			if schema.matches(payload) {
				selected = i
			}
		}
		if selected < 0 {
			return Event{}, errors.New("no schema")
		}
		var err error
		event, err = schemas[selected].adapt(event)
		if err != nil {
			return Event{}, err
		}
	}
	return prepareEventWithRegistration(event, registration)
}

func prepareEventWithRegistration(event Event, registration EventKindRegistration) (Event, error) {
	return upcastEventWithRegistration(event, registration)
}

func appendEvent(db *sql.DB, event Event) error {
	_, err := db.Exec("INSERT INTO domain_events (kind, payload_version, payload) VALUES (?, ?, ?)", event.Kind, event.PayloadVersion, event.Payload)
	return err
}

func RecordA(db *sql.DB, payload []byte) error {
	return appendEvent(db, Event{Kind: "thing.a", SubjectType: SubjectProduct, PayloadVersion: 2, Payload: payload})
}

func ReadEvent(db *sql.DB) (Event, error) {
	var event Event
	err := db.QueryRow("SELECT kind, payload_version, payload FROM domain_events").Scan(&event.Kind, &event.PayloadVersion, &event.Payload)
	if err != nil {
		return Event{}, err
	}
	if err := validateRegistry(); err != nil {
		return Event{}, err
	}
	return prepareRecordedEvent(event)
}
`

const fixtureMain = `package main

import (
	"bytes"
	"database/sql"
	"reflect"

	"example.com/fixture/internal/store"
)

func reconstruct(existing, requested store.Event) (store.Event, error) {
	requested.PayloadVersion = existing.PayloadVersion
	return requested, nil
}

func same(existing, requested store.Event) bool {
	if existing.Kind != requested.Kind || existing.PayloadVersion != requested.PayloadVersion {
		return false
	}
	return bytes.Equal(existing.Payload, requested.Payload) && reflect.DeepEqual(existing.SubjectType, requested.SubjectType)
}

func keep(store.Event) {}

func main() {
	db, _ := sql.Open("sqlite", "")
	existing, err := store.ReadEvent(db)
	if err != nil {
		return
	}
	acknowledgment, err := reconstruct(existing, store.Event{Kind: existing.Kind})
	if err != nil || !same(existing, acknowledgment) {
		return
	}
	_ = store.RecordA(db, nil)
}
`

const fixtureModule = "example.com/fixture"

// mutation replaces old with new in one fixture file; old must occur.
type mutation struct {
	file, old, new string
}

// analyzeFixtureRefusal analyzes the mutated fixture and returns any refusal.
func analyzeFixtureRefusal(t *testing.T, mutations ...mutation) writerAnalysis {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module " + fixtureModule + "\n\ngo 1.26\n", "internal/store/store.go": fixtureStore, "cmd/concord/main.go": fixtureMain}
	for _, m := range mutations {
		if !strings.Contains(files[m.file], m.old) {
			t.Fatalf("mutation anchor %q is not in %s", m.old, m.file)
		}
		files[m.file] = strings.Replace(files[m.file], m.old, m.new, 1)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return analyzeWriters(dir)
}

func analyzeFixture(t *testing.T, mutations ...mutation) writerAnalysis {
	t.Helper()
	analysis := analyzeFixtureRefusal(t, mutations...)
	if analysis.Refused != "" {
		t.Fatalf("fixture analysis refused: %s", analysis.Refused)
	}
	return analysis
}

const (
	storePkg = fixtureModule + "/internal/store"
	mainPkg  = fixtureModule + "/cmd/concord"
)

// site requires an open site of function with that expression.
func site(t *testing.T, analysis writerAnalysis, function, expression string) {
	t.Helper()
	for _, s := range analysis.Open {
		if s.Function == function && s.Expression == expression {
			return
		}
	}
	t.Fatalf("no open site %s %q in %+v", function, expression, analysis.Open)
}

func TestFixtureAnalysisListsEachSite(t *testing.T) {
	analysis := analyzeFixture(t)
	var got []string
	for _, s := range analysis.Open {
		if s.FunctionDigest == "" {
			t.Errorf("site %+v", s)
		}
		got = append(got, strings.TrimPrefix(strings.TrimPrefix(s.Function, storePkg+"."), mainPkg+".")+": "+s.Expression+" "+strings.Join(s.Pairs, ","))
	}
	want := []string{
		`reconstruct: requested.PayloadVersion = existing.PayloadVersion * v*`,
		`ReadEvent: &event.Kind * v*`,
		`ReadEvent: &event.PayloadVersion * v*`,
		`upcastA1: event.PayloadVersion = 2 * v2`,
		`upcastB1: event.PayloadVersion = 2 * v2`,
		`upcastB2: event.PayloadVersion = 3 * v3`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sites:\n%s", strings.Join(got, "\n"))
	}
	if !slices.Equal(analysis.Writes, []string{"thing.a v2"}) {
		t.Fatalf("writes = %q", analysis.Writes)
	}
	if !slices.Equal(analysis.WorkItemFolds, []string{"thing.b"}) || analysis.CheckSubject == "" {
		t.Fatalf("work-item folds %q, check subject %q", analysis.WorkItemFolds, analysis.CheckSubject)
	}
	if d := analysis.Dispatcher; d == nil || d.Function != storePkg+".upcastEventWithRegistration" || !slices.Equal(d.Guards, []string{"event.PayloadVersion < registration.MinSupported || event.PayloadVersion > registration.CurrentVersion", "upcaster == nil", "err != nil"}) {
		t.Fatalf("dispatcher %+v", analysis.Dispatcher)
	}
	var callers []string
	for _, c := range analysis.Dispatcher.Callers {
		callers = append(callers, strings.TrimPrefix(c.Function, storePkg+"."))
	}
	if !slices.Equal(callers, []string{"prepareEventWithRegistration", "prepareRecordedEvent"}) {
		t.Fatalf("dispatcher callers %q", callers)
	}
	// upcastB2 is also a recorded-schema adapt value: a function value.
	var confined []string
	for _, s := range analysis.Open {
		if s.Confinement != "" {
			confined = append(confined, strings.TrimPrefix(s.Function, storePkg+".")+" "+s.Confinement)
		}
	}
	if !slices.Equal(confined, []string{"upcastA1 upcaster", "upcastB1 upcaster"}) {
		t.Fatalf("confined %q", confined)
	}
}

// An upcaster is called through Upcaster values, so a static call does not
// bind its parameter: its site stays open.
func TestUpcasterIsNotContextual(t *testing.T) {
	analysis := analyzeFixture(t, mutation{"internal/store/store.go", "func ReadEvent(", "func Stray() (Event, error) { return upcastA1(Event{Kind: \"thing.a\", PayloadVersion: 1}) }\n\nfunc ReadEvent("})
	i := slices.IndexFunc(analysis.Open, func(s writerSite) bool {
		return s.Function == storePkg+".upcastA1" && s.Expression == "event.PayloadVersion = 2"
	})
	if i < 0 || !slices.Equal(analysis.Open[i].Pairs, []string{"* v2"}) || analysis.Open[i].Confinement != "" {
		t.Fatalf("open %+v", analysis.Open)
	}
}

// An upcaster site stays confined only while the function is reachable only
// as an Upcaster value through the one dispatcher, and its own Event
// parameter keeps its kind and after the site is only returned.
func TestUpcasterConfinement(t *testing.T) {
	const store = "internal/store/store.go"
	const upcastA1 = "func upcastA1(event Event) (Event, error) {\n\tevent.PayloadVersion = 2\n\treturn event, nil\n}"
	body := func(code string) mutation {
		return mutation{store, upcastA1, "func upcastA1(event Event) (Event, error) {\n" + code + "\n}"}
	}
	declare := func(code string) mutation { return mutation{store, "func ReadEvent(", code + "\n\nfunc ReadEvent("} }
	for name, c := range map[string]struct {
		mutations  []mutation
		expression string
		confined   bool
	}{
		"payload beside the version":  {[]mutation{body("\tevent.Payload, event.PayloadVersion = nil, 2\n\t_ = event.Kind\n\treturn event, nil")}, "event.Payload, event.PayloadVersion = nil, 2", true},
		"call outside the dispatcher": {[]mutation{declare("func Stray(event Event) (Event, error) { return eventKindRegistry[\"thing.a\"].Upcasters[1](event) }")}, "", false},
		"upcaster handed on":          {[]mutation{declare("func Stray() Upcaster { return eventKindRegistry[\"thing.a\"].Upcasters[1] }")}, "", false},
		"comma-ok into a variable":    {[]mutation{declare("func Stray(event Event) (Event, error) {\n\tvar f func(Event) (Event, error)\n\tf, _ = eventKindRegistry[\"thing.a\"].Upcasters[1]\n\treturn f(event)\n}")}, "", false},
		"range into a variable":       {[]mutation{declare("func Stray(event Event) (Event, error) {\n\tvar f func(Event) (Event, error)\n\tfor _, f = range eventKindRegistry[\"thing.a\"].Upcasters {\n\t}\n\treturn f(event)\n}")}, "", false},
		"generic caller":              {[]mutation{declare("func apply[F ~func(Event) (Event, error)](fs map[int]F, event Event) (Event, error) { return fs[1](event) }\n\nfunc Stray(event Event) (Event, error) { return apply(eventKindRegistry[\"thing.a\"].Upcasters, event) }")}, "", false},
		"function used as a value":    {[]mutation{declare("var stray = upcastA1")}, "", false},
		"function also called":        {[]mutation{declare("func Stray(event Event) (Event, error) { return upcastA1(event) }")}, "", false},
		"kind assigned":               {[]mutation{body("\tevent.Kind = \"thing.b\"\n\tevent.PayloadVersion = 2\n\treturn event, nil")}, "", false},
		"kind beside the version":     {[]mutation{body("\tevent.Kind, event.PayloadVersion = \"thing.b\", 2\n\treturn event, nil")}, "event.Kind, event.PayloadVersion = \"thing.b\", 2", false},
		"another holder first":        {[]mutation{body("\tother := Event{}\n\tother.Kind, event.PayloadVersion = \"thing.b\", 2\n\t_ = other\n\treturn event, nil")}, "other.Kind, event.PayloadVersion = \"thing.b\", 2", false},
		"named result":                {[]mutation{{store, upcastA1, "func upcastA1(event Event) (out Event, err error) {\n\tdefer func() { sink(out) }()\n\tevent.PayloadVersion = 2\n\treturn event, nil\n}"}, declare("func sink(Event) {}")}, "", false},
		"goto back over the site":     {[]mutation{body("\tn := 0\nagain:\n\tif n == 1 {\n\t\tsink(event)\n\t\treturn event, nil\n\t}\n\tevent.PayloadVersion = 2\n\tn = 1\n\tgoto again"), declare("func sink(Event) {}")}, "", false},
		"site in a closure":           {[]mutation{body("\tfunc() { event.PayloadVersion = 2 }()\n\treturn event, nil")}, "", false},
		"three-clause loop":           {[]mutation{body("\tfor i := 0; i < 1; i++ {\n\t\tevent.PayloadVersion = 2\n\t}\n\treturn event, nil")}, "", false},
		"method value after the site": {[]mutation{body("\tevent.PayloadVersion = 2\n\tf := event.Copy\n\t_ = f\n\treturn event, nil"), declare("func (e Event) Copy() Event { return e }")}, "", false},
		"parameter replaced":          {[]mutation{body("\tevent = Event{Payload: event.Payload}\n\tevent.PayloadVersion = 2\n\treturn event, nil")}, "", false},
		"parameter addressed":         {[]mutation{body("\tevent.PayloadVersion = 2\n\tkeep(&event)\n\treturn event, nil"), declare("func keep(*Event) {}")}, "", false},
		"parameter captured":          {[]mutation{body("\tevent.PayloadVersion = 2\n\tfunc() { _ = event }()\n\treturn event, nil")}, "", false},
		"site in a loop":              {[]mutation{body("\tfor range 1 {\n\t\tevent.PayloadVersion = 2\n\t}\n\treturn event, nil")}, "", false},
		"handed on after the site":    {[]mutation{body("\tevent.PayloadVersion = 2\n\tsink(event)\n\treturn event, nil"), declare("func sink(Event) {}")}, "", false},
		"returned through a call":     {[]mutation{body("\tevent.PayloadVersion = 2\n\treturn pick(event, event)"), declare("func pick(a, b Event) (Event, error) { return a, nil }")}, "", false},
		"addressed before the site":   {[]mutation{body("\tp := &event\n\tevent.PayloadVersion = 2\n\tsink(*p)\n\treturn event, nil"), declare("func sink(Event) {}")}, "", false},
		"captured before the site":    {[]mutation{body("\tf := func() Event { return event }\n\tevent.PayloadVersion = 2\n\tsink(f())\n\treturn event, nil"), declare("func sink(Event) {}")}, "", false},
		"another local":               {[]mutation{body("\tother := event\n\tother.PayloadVersion = 2\n\treturn other, nil")}, "other.PayloadVersion = 2", false},
		"dispatcher used as a value":  {[]mutation{declare("var stray = upcastEventWithRegistration")}, "", false},
		"forwarder used as a value":   {[]mutation{declare("var stray = prepareEventWithRegistration")}, "", false},
		"dispatcher call in closure":  {[]mutation{declare("var stray = func(event Event) (Event, error) { return upcastEventWithRegistration(event, eventKindRegistry[\"thing.b\"]) }")}, "", false},
		"no dispatcher":               {[]mutation{declare("func Stray(u Upcaster, event Event) (Event, error) { return u(event) }")}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			expression := c.expression
			if expression == "" {
				expression = "event.PayloadVersion = 2"
			}
			analysis := analyzeFixture(t, c.mutations...)
			i := slices.IndexFunc(analysis.Open, func(s writerSite) bool { return s.Function == storePkg+".upcastA1" && s.Expression == expression })
			if i < 0 {
				t.Fatalf("no site %q in %+v", expression, analysis.Open)
			}
			if got := analysis.Open[i].Confinement; (got == "upcaster") != c.confined {
				t.Fatalf("confinement %q; dispatcher %+v; open %+v", got, analysis.Dispatcher, analysis.Open)
			}
		})
	}
}

// The analysis proves the recorded-schema adapt route: the one adapt caller,
// and every adapt result passing through the forwarder to the dispatcher.
// The analysis proves the recorded-schema adapt route: the one adapt caller,
// and every adapt result passing through the forwarder to the dispatcher.
// Each escape a reviewed route must exclude leaves no route.
func TestAdaptRoute(t *testing.T) {
	const store = "internal/store/store.go"
	analysis := analyzeFixture(t)
	route := analysis.Adapt
	if route == nil || route.Function != storePkg+".prepareRecordedEvent" {
		t.Fatalf("adapt route %+v", route)
	}
	if route.FunctionDigest == "" || !slices.Equal(route.Forwarders, []namedHash{{storePkg + ".prepareEventWithRegistration", route.Forwarders[0].Digest}}) {
		t.Fatalf("adapt route %+v", route)
	}
	// The registry names upcastB2 as an adapt value, so the route pins its
	// digest: a changed adapt function must change the pinned value set.
	if !slices.ContainsFunc(route.Values, func(v namedHash) bool { return v.Function == storePkg+".upcastB2" && v.Digest != "" }) {
		t.Fatalf("adapt route values %v", route.Values)
	}
	mutated := analyzeFixture(t, mutation{store, "func upcastB2(event Event) (Event, error) {\n\tevent.PayloadVersion = 3\n\treturn event, nil\n}", "func upcastB2(event Event) (Event, error) {\n\tevent.PayloadVersion = 3\n\tevent.Payload = nil\n\treturn event, nil\n}"})
	if mutated.Adapt == nil || slices.Equal(mutated.Adapt.Values, route.Values) {
		t.Fatalf("a mutated adapt value must change the pinned digests: %+v", mutated.Adapt)
		elided := analyzeFixture(t, mutation{store, "RecordedSchemas map[int][]recordedPayloadSchema", "RecordedSchemas map[int][]*recordedPayloadSchema"}, mutation{store, "RecordedSchemas: map[int][]recordedPayloadSchema{", "RecordedSchemas: map[int][]*recordedPayloadSchema{"})
		if elided.Adapt == nil || !slices.Equal(elided.Adapt.Values, route.Values) {
			t.Fatalf("an elided &recordedPayloadSchema literal must pin its adapt value: %+v", elided.Adapt)
		}
		delegating := analyzeFixture(t, mutation{store, "func upcastB2(event Event) (Event, error) {\n\tevent.PayloadVersion = 3\n\treturn event, nil\n}", "func upcastB2(event Event) (Event, error) {\n\treturn adaptDelegate(event)\n}\n\nfunc adaptDelegate(event Event) (Event, error) {\n\tevent.PayloadVersion = 3\n\treturn event, nil\n}"})
		if delegating.Adapt == nil || !slices.ContainsFunc(delegating.Adapt.Values, func(v namedHash) bool { return v.Function == storePkg+".adaptDelegate" }) {
			t.Fatalf("a value that delegates its event must pin its callee: %+v", delegating.Adapt)
		}
	}
	// tail replaces prepareRecordedEvent's adapt tail; escape runs between
	// the adapt assignment and the forwarder call.
	tail := func(escape string) mutation {
		old := "\t\tevent, err = schemas[selected].adapt(event)\n\t\tif err != nil {\n\t\t\treturn Event{}, err\n\t\t}\n\t}\n\treturn prepareEventWithRegistration(event, registration)\n}"
		now := "\t\tevent, err = schemas[selected].adapt(event)\n\t\tif err != nil {\n\t\t\treturn Event{}, err\n\t\t}\n\t\t" + escape + "\n\t}\n\treturn prepareEventWithRegistration(event, registration)\n}"
		return mutation{store, old, now}
	}
	written := tail("if err := appendEvent(nil, event); err != nil {\n\t\t\treturn Event{}, err\n\t\t}")
	compared := tail("if event.Kind == \"thing.b\" {\n\t\t\treturn Event{}, nil\n\t\t}")
	handAdapt := tail("if _, err := handAdapt(schemas[selected].adapt, event); err != nil {\n\t\t\treturn Event{}, err\n\t\t}")
	for name, mutations := range map[string][]mutation{
		"adapt result written":   {written},
		"adapt result returned":  {mutation{store, "\treturn prepareEventWithRegistration(event, registration)\n}", "\treturn event, nil\n}"}},
		"adapt result compared":  {compared},
		"forwarder leaks result": {mutation{store, "return upcastEventWithRegistration(event, registration)\n}", "sink(event)\n\treturn upcastEventWithRegistration(event, registration)\n}"}, mutation{store, "func ReadEvent(", "func sink(Event) {}\n\nfunc ReadEvent("}},
		"second adapt caller":    {mutation{store, "func ReadEvent(", "func secondAdaptCaller(schemas []recordedPayloadSchema, event Event) (Event, error) { return schemas[0].adapt(event) }\n\nfunc ReadEvent("}},
		"adapt result in a loop": {mutation{store, "\tif schemas := registration.RecordedSchemas[event.PayloadVersion]; len(schemas) != 0 {", "\tif schemas := registration.RecordedSchemas[event.PayloadVersion]; len(schemas) != 0 {\n\tfor i := 0; i < 1; i++ {"}, mutation{store, "\t\t}\n\t}\n\treturn prepareEventWithRegistration(event, registration)\n}", "\t\t}\n\t\t}\n\t}\n\treturn prepareEventWithRegistration(event, registration)\n}"}},
		"adapt target captured":  {mutation{store, "\t\tevent, err = schemas[selected].adapt(event)", "\t\tcapture = func() { _ = event }\n\t\tevent, err = schemas[selected].adapt(event)"}, mutation{store, "func ReadEvent(", "var capture func()\n\nfunc ReadEvent("}},
		"adapt field handed on":  {mutation{store, "func ReadEvent(", "func handAdapt(adapt func(Event) (Event, error), event Event) (Event, error) { return adapt(event) }\n\nfunc ReadEvent("}, handAdapt},
		"adapt value a literal":  {mutation{store, "adapt: upcastB2}", "adapt: func(event Event) (Event, error) { return event, nil }}"}},
		"adapt value a variable": {mutation{store, "adapt: upcastB2}", "adapt: heldAdapt}"}, mutation{store, "func ReadEvent(", "var heldAdapt = upcastB2\n\nfunc ReadEvent("}},
	} {
		t.Run(name, func(t *testing.T) {
			if analysis := analyzeFixture(t, mutations...); analysis.Adapt != nil {
				t.Fatalf("adapt route survived: %+v", analysis.Adapt)
			}
		})
	}
}

// A comparison-only site's function returns an Event that its one caller
// binds and feeds only to a bool comparator. Each escape leaves no route.
func TestComparisonSite(t *testing.T) {
	const main = "cmd/concord/main.go"
	analysis := analyzeFixture(t)
	var route *comparisonRoute
	found := false
	for _, s := range analysis.Open {
		if s.Comparison != nil && strings.HasSuffix(s.Function, ".reconstruct") {
			found = true
			route = s.Comparison
		}
	}
	if !found || route.Caller != mainPkg+".main" || route.Comparator != mainPkg+".same" || route.CallerDigest == "" || route.ComparatorDigest == "" {
		t.Fatalf("comparison route %+v found=%v", route, found)
	}
	t.Run("call inside a closure pins the enclosing function", func(t *testing.T) {
		analysis := analyzeFixture(t, mutation{main,
			"acknowledgment, err := reconstruct(existing, store.Event{Kind: existing.Kind})\n\tif err != nil || !same(existing, acknowledgment) {\n\t\treturn\n\t}",
			"func() {\n\t\tacknowledgment, err := reconstruct(existing, store.Event{Kind: existing.Kind})\n\t\tif err == nil {\n\t\t\tsame(existing, acknowledgment)\n\t\t}\n\t}()"})
		closureFound := false
		for _, s := range analysis.Open {
			if s.Comparison != nil && strings.HasSuffix(s.Function, ".reconstruct") {
				closureFound = true
				if s.Comparison.Caller != mainPkg+".main" || s.Comparison.Comparator != mainPkg+".same" {
					t.Fatalf("closure comparison route %+v", s.Comparison)
				}
			}
		}
		if !closureFound {
			t.Fatal("comparison route lost when the call sits in a closure")
		}
	})
	for name, mutations := range map[string][]mutation{
		"result handed on":               {mutation{main, "acknowledgment, err := reconstruct(existing, store.Event{Kind: existing.Kind})", "acknowledgment, err := reconstruct(existing, store.Event{Kind: existing.Kind})\n\tkeep(acknowledgment)"}},
		"second caller":                  {mutation{main, "func main() {", "func again(existing, requested store.Event) bool {\n\tack, err := reconstruct(existing, requested)\n\treturn err == nil && same(existing, ack)\n}\n\nfunc main() {"}},
		"function value":                 {mutation{main, "func reconstruct(", "var _ = reconstruct\n\nfunc reconstruct("}},
		"comparator writes its event":    {mutation{main, "func same(existing, requested store.Event) bool {", "func same(existing, requested store.Event) bool {\n\trequested.Kind = existing.Kind"}},
		"comparator addresses its event": {mutation{main, "func same(existing, requested store.Event) bool {", "func same(existing, requested store.Event) bool {\n\talias := &requested\n\t_ = alias.Kind"}},
		"comparator leaks its event":     {mutation{main, "return bytes.Equal(existing.Payload, requested.Payload) && reflect.DeepEqual(existing.SubjectType, requested.SubjectType)", "keep(requested)\n\treturn bytes.Equal(existing.Payload, requested.Payload) && reflect.DeepEqual(existing.SubjectType, requested.SubjectType)"}},
		"target held by a pointer":       {mutation{main, "func reconstruct(existing, requested store.Event) (store.Event, error) {\n\trequested.PayloadVersion = existing.PayloadVersion\n\treturn requested, nil\n}", "func reconstruct(existing, requested store.Event) (store.Event, error) {\n\tpointer := &requested\n\tpointer.PayloadVersion = existing.PayloadVersion\n\treturn requested, nil\n}"}},
		"target captured early":          {mutation{main, "func reconstruct(existing, requested store.Event) (store.Event, error) {", "func reconstruct(existing, requested store.Event) (store.Event, error) {\n\thold := func() { _ = requested.Kind }\n\t_ = hold"}},
		"site inside a loop":             {mutation{main, "func reconstruct(existing, requested store.Event) (store.Event, error) {", "func reconstruct(existing, requested store.Event) (store.Event, error) {\n\tfor i := 0; i < 1; i++ {"}, mutation{main, "\trequested.PayloadVersion = existing.PayloadVersion\n\treturn requested, nil\n}", "\t\trequested.PayloadVersion = existing.PayloadVersion\n\t}\n\treturn requested, nil\n}"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, s := range analyzeFixture(t, mutations...).Open {
				if s.Comparison != nil {
					t.Fatalf("comparison route survived: %+v", s.Comparison)
				}
			}
		})
	}
}

// A function that calls the dispatcher once with a lookup and once with its
// own registration parameter forwards it: its callers are dispatcher callers.
func TestDispatcherCallersFollowEveryForwardingCall(t *testing.T) {
	twice := "func twice(event Event, registration EventKindRegistration) (Event, error) {\n\tif _, err := upcastEventWithRegistration(event, eventKindRegistry[event.Kind]); err != nil {\n\t\treturn Event{}, err\n\t}\n\treturn upcastEventWithRegistration(event, registration)\n}\n\nfunc Stray(event Event) (Event, error) { return twice(event, eventKindRegistry[\"thing.b\"]) }"
	analysis := analyzeFixture(t, mutation{"internal/store/store.go", "func ReadEvent(", twice + "\n\nfunc ReadEvent("})
	if analysis.Dispatcher == nil {
		t.Fatal("no dispatcher")
	}
	var callers []string
	for _, c := range analysis.Dispatcher.Callers {
		callers = append(callers, strings.TrimPrefix(c.Function, storePkg+"."))
	}
	if !slices.Equal(callers, []string{"Stray", "prepareEventWithRegistration", "prepareRecordedEvent", "twice"}) {
		t.Fatalf("dispatcher callers %q", callers)
	}
}

// The analysis pins the registry-lookup helper the reviewed callers pass
// event.Kind through by its exact declaration bytes: the recorded digest
// follows the helper's body while every dispatcher key stays fixed, and the
// pin serializes into the analysis.
func TestRegisteredKindPinFollowsTheDeclaration(t *testing.T) {
	const store = "internal/store/store.go"
	analysis := analyzeFixture(t)
	d := analysis.Dispatcher
	if d == nil || d.RegisteredKind == nil || d.RegisteredKind.Function != storePkg+".registeredEventKind" {
		t.Fatalf("dispatcher %+v", d)
	}
	drifted := analyzeFixture(t, mutation{store, "return registration, ok", "return registration, ok // the reviewed lookup"})
	if drifted.Dispatcher == nil || drifted.Dispatcher.RegisteredKind == nil ||
		drifted.Dispatcher.RegisteredKind.Digest == d.RegisteredKind.Digest ||
		drifted.Dispatcher.RegisteredKind.Function != d.RegisteredKind.Function ||
		drifted.Dispatcher.Function != d.Function || drifted.Dispatcher.FunctionDigest != d.FunctionDigest ||
		!slices.Equal(drifted.Dispatcher.Guards, d.Guards) ||
		!slices.Equal(drifted.Dispatcher.Callers, d.Callers) {
		t.Fatalf("drifted %+v want %+v", drifted.Dispatcher, d)
	}
	data, err := json.Marshal(analysis)
	if err != nil || !strings.Contains(string(data), `"registered_event_kind"`) {
		t.Fatalf("analysis serialization %s: %v", data, err)
	}
}

// A fold refuses a work_item subject only when the registration sets it,
// directly or through constructors, and it begins with a checkSubject guard
// on its Event parameter for a constant other than SubjectWorkItem.
func TestWorkItemFoldsNeedAProvenRefusal(t *testing.T) {
	const store = "internal/store/store.go"
	const foldB = "func foldB(tx *Tx, state *State, event Event) error { return nil }"
	guard := func(body string) mutation {
		return mutation{store, foldB, "func foldB(tx *Tx, state *State, event Event) error {\n" + body + "\n\treturn nil\n}"}
	}
	const refusal = "\tif err := checkSubject(event, SubjectProduct); err != nil {\n\t\treturn err\n\t}"
	registered := func(entries, extra string) []mutation {
		return []mutation{
			{store, `"thing.b": {CurrentVersion: 3`, entries + "\n\t\"thing.b\": {CurrentVersion: 3"},
			{store, "func upcastA1(", `func register(fold func(*Tx, *State, Event) error) EventKindRegistration {
	return EventKindRegistration{CurrentVersion: 1, MinSupported: 1, Fold: fold}
}

func scoped(fold func(*Tx, *State, Event) error) EventKindRegistration {
` + extra + `	return register(fold)
}

func upcastA1(`},
		}
	}
	declare := func(entry, decls string) []mutation {
		return []mutation{
			{store, `"thing.b": {CurrentVersion: 3`, entry + "\n\t\"thing.b\": {CurrentVersion: 3"},
			{store, "func upcastA1(", decls + "\n\nfunc upcastA1("},
		}
	}
	for name, c := range map[string]struct {
		mutations []mutation
		want      []string
	}{
		"through constructors":   {registered(`"thing.c": scoped(foldA),`+"\n\t"+`"thing.d": scoped(foldB),`, ""), []string{"thing.b", "thing.d"}},
		"method expression":      {[]mutation{{store, `"thing.b": {CurrentVersion: 3`, `"thing.c": foldFunc.with(foldA, foldB),` + "\n\t\"thing.b\": {CurrentVersion: 3"}, {store, "func upcastA1(", "type foldFunc func(*Tx, *State, Event) error\n\nfunc (f foldFunc) with(fold func(*Tx, *State, Event) error) EventKindRegistration {\n\treturn EventKindRegistration{Fold: fold}\n}\n\nfunc upcastA1("}}, []string{"thing.b", "thing.c"}},
		"constructor reassigns":  {registered(`"thing.c": scoped(foldA),`, "\tfold = foldB\n"), []string{"thing.b", "thing.c"}},
		"both refuse":            {[]mutation{guard(refusal)}, nil},
		"guard for work_item":    {[]mutation{guard(strings.Replace(refusal, "SubjectProduct", "SubjectWorkItem", 1))}, []string{"thing.b"}},
		"guard ignored":          {[]mutation{guard(strings.Replace(refusal, "return err", "return nil", 1))}, []string{"thing.b"}},
		"guard not first":        {[]mutation{guard("\t_ = state\n" + refusal)}, []string{"thing.b"}},
		"guard on another event": {[]mutation{guard(strings.Replace(refusal, "(event,", "(blank,", 1)), {store, "func upcastA1(", "var blank Event\n\nfunc upcastA1("}}, []string{"thing.b"}},
		"registry written":       {[]mutation{guard(refusal), {store, "func upcastA1(", "func init() { eventKindRegistry[\"thing.c\"] = EventKindRegistration{Fold: foldB} }\n\nfunc upcastA1("}}, []string{"*"}},
		"deferred constructor":   {declare(`"thing.c": deferred(foldA),`, "func deferred(fold func(*Tx, *State, Event) error) (r EventKindRegistration) {\n\tdefer func() { r.Fold = foldB }()\n\treturn EventKindRegistration{Fold: fold}\n}"), []string{"thing.b", "thing.c"}},
		"returns disagree":       {declare(`"thing.c": either(foldA, true),`, "func either(fold func(*Tx, *State, Event) error, other bool) EventKindRegistration {\n\tif other {\n\t\treturn EventKindRegistration{Fold: foldB}\n\t}\n\treturn EventKindRegistration{Fold: fold}\n}"), []string{"thing.b", "thing.c"}},
		"address taken":          {declare(`"thing.c": taken(foldA),`, "func taken(fold func(*Tx, *State, Event) error) EventKindRegistration {\n\tswap(&fold)\n\treturn EventKindRegistration{Fold: fold}\n}\n\nfunc swap(fold *func(*Tx, *State, Event) error) { *fold = foldB }"), []string{"thing.b", "thing.c"}},
		"condition inverted":     {[]mutation{guard(strings.Replace(refusal, "err != nil", "err == nil", 1))}, []string{"thing.b"}},
		"another guard":          {[]mutation{guard(strings.Replace(refusal, "checkSubject(", "check(", 1)), {store, "func upcastA1(", "func check(event Event, want SubjectType) error { return nil }\n\nfunc upcastA1("}}, []string{"thing.b"}},
		"non-constant key":       {declare("dynamicKind: {Fold: foldA},", "var dynamicKind = \"thing.c\""), []string{"*"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := analyzeFixture(t, c.mutations...).WorkItemFolds; !slices.Equal(got, c.want) {
				t.Fatalf("work-item folds %q, want %q", got, c.want)
			}
		})
	}
}

// Each mutation adds one way to set an Event's kind or version; each must
// appear as one open site.
func TestEachWriteFormIsASite(t *testing.T) {
	const anchor = "func ReadEvent("
	for name, c := range map[string]struct{ code, function, expression string }{
		"range statement": {`func Ranged(event *Event, kinds []string) {
	for _, event.Kind = range kinds {
	}
}`, "Ranged", "event.Kind"},
		"field increment": {`func Bump(event *Event) { event.PayloadVersion++ }`, "Bump", "event.PayloadVersion++"},
		"field address":   {`func Point(event *Event) *int { return &event.PayloadVersion }`, "Point", "&event.PayloadVersion"},
		"tuple assignment": {`func Pair(event *Event) { event.Kind, event.PayloadVersion = pair() }

func pair() (string, int) { return "thing.a", 1 }`, "Pair", `event.Kind, event.PayloadVersion = pair()`},
		"positional literal":     {`func Positional() Event { return Event{"thing.a", SubjectProduct, 1, nil} }`, "Positional", `Event{"thing.a", SubjectProduct, 1, nil}`},
		"literal eliding &Event": {`func Elided(kind string) []*Event { return []*Event{{Kind: kind, PayloadVersion: 1}} }`, "Elided", `{Kind: kind, PayloadVersion: 1}`},
		"conversion into an Event": {`type raw struct {
	Kind           string
	SubjectType    SubjectType
	PayloadVersion int
	Payload        []byte
}

func Convert(r raw) Event { return Event(r) }`, "Convert", "Event(r)"},
		"holder handed to an interface": {`func Decode(data []byte, event *Event) error { return json.Unmarshal(data, event) }`, "Decode", "json.Unmarshal(data, event)"},
		"holder assigned to an interface": {`func Hold(event *Event) any {
	var held any
	held = event
	return held
}`, "Hold", "held"},
		"holder returned as an interface": {`func Expose(event *Event) any { return event }`, "Expose", "return event"},
		"holder in an interface field": {`type box struct{ value any }

func Box(event *Event) box { return box{value: event} }`, "Box", "event"},
		"update statement": {`func Rewrite(db *sql.DB) error {
	_, err := db.Exec("UPDATE domain_events SET payload_version = 1")
	return err
}`, "Rewrite", `"UPDATE domain_events SET payload_version = 1"`},
		"insert of other values": {`func Forge(db *sql.DB, event Event) error {
	_, err := db.Exec("INSERT INTO domain_events (kind, payload_version, payload) VALUES (?, 1, ?)", event.Kind, event.Payload)
	return err
}`, "Forge", `"INSERT INTO domain_events (kind, payload_version, payload) VALUES (?, 1, ?)"`},
		"insert of two Events' fields": {`func Mix(db *sql.DB, a, b Event) error {
	_, err := db.Exec("INSERT INTO domain_events (kind, payload_version, payload) VALUES (?, ?, ?)", a.Kind, b.PayloadVersion, a.Payload)
	return err
}`, "Mix", `"INSERT INTO domain_events (kind, payload_version, payload) VALUES (?, ?, ?)"`},
		"pointer conversion into an Event": {`type raw struct {
	Kind           string
	SubjectType    SubjectType
	PayloadVersion int
	Payload        []byte
}

func Cast(r *raw) *Event { return (*Event)(r) }`, "Cast", "(*Event)(r)"},
		"holder as an interface map key":              {`func Key(event *Event) map[any]bool { return map[any]bool{event: true} }`, "Key", "event"},
		"holder indexing an interface-keyed map":      {`func Index(seen map[any]bool, event *Event) { seen[event] = true }`, "Index", "seen[event]"},
		"map with holder keys handed to an interface": {`func Keys(events map[*Event]bool) ([]byte, error) { return json.Marshal(events) }`, "Keys", "json.Marshal(events)"},
		"channel of holders handed to an interface":   {`func Chan(events chan *Event) ([]byte, error) { return json.Marshal(events) }`, "Chan", "json.Marshal(events)"},
		"holder sent on an interface channel":         {`func Send(events chan any, event *Event) { events <- event }`, "Send", "events <- event"},
		"holder declared as an interface": {`func Declare(event *Event) any {
	var held any = event
	return held
}`, "Declare", "held"},
		"holder spread into interface variables": {`func Unpack(event *Event) (any, error) {
	var held any
	var err error
	held, err = hold(event)
	return held, err
}

func hold(event *Event) (*Event, error) { return event, nil }`, "Unpack", "held"},
		"holder spread into a call": {`func Show(event *Event) { show(hold(event)) }

func hold(event *Event) (*Event, error) { return event, nil }

func show(values ...any) {}`, "Show", "show(hold(event))"},
		"holder spread into interface results": {`func Spread(event *Event) (any, error) { return hold(event) }

func hold(event *Event) (*Event, error) { return event, nil }`, "Spread", "return hold(event)"},
		"holder returned from a function literal": {`func Later(event *Event) func() any { return func() any { return event } }`, "Later", "return event"},
		"constant concatenation": {`func Concat(db *sql.DB) error {
	const rewrite = "UPDATE " + "domain_events SET kind = 'thing.b'"
	_, err := db.Exec(rewrite)
	return err
}`, "Concat", `"UPDATE " + "domain_events SET kind = 'thing.b'"`},
		"quoted, schema-qualified table": {`func Quoted(db *sql.DB) error {
	_, err := db.Exec("UPDATE \"main\".\"domain_events\" SET kind = 'thing.b'")
	return err
}`, "Quoted", `"UPDATE \"main\".\"domain_events\" SET kind = 'thing.b'"`},
		"insert or replace": {`func Upsert(db *sql.DB, event Event) error {
	_, err := db.Exec("INSERT OR REPLACE INTO domain_events (kind, payload_version) VALUES (?, ?)", event.Kind, event.PayloadVersion)
	return err
}`, "Upsert", `"INSERT OR REPLACE INTO domain_events (kind, payload_version) VALUES (?, ?)"`},
		"insert naming kind twice": {`func Twice(db *sql.DB, event Event) error {
	_, err := db.Exec("INSERT INTO domain_events (KIND, kind, payload_version) VALUES (?, ?, ?)", "thing.b", event.Kind, event.PayloadVersion)
	return err
}`, "Twice", `"INSERT INTO domain_events (KIND, kind, payload_version) VALUES (?, ?, ?)"`},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixture(t, mutation{"internal/store/store.go", anchor, c.code + "\n\n" + anchor})
			var sites []string
			for _, s := range analysis.Open {
				if s.Function == storePkg+"."+c.function {
					sites = append(sites, s.Expression)
				}
			}
			if !slices.Equal(sites, []string{c.expression}) {
				t.Fatalf("sites of %s = %q", c.function, sites)
			}
		})
	}
}

// A site's pairs resolve through constants and locals. A fully resolved site
// adds writes; a site with any unresolved value stays open with its pairs.
func TestSitePairsResolve(t *testing.T) {
	const anchor = "func ReadEvent("
	for name, c := range map[string]struct {
		code   string
		writes []string
		open   []string
	}{
		"locals": {`func F() Event {
	kind := "thing.b"
	var version = 3
	return Event{Kind: kind, PayloadVersion: version}
}`, []string{"thing.b v3"}, nil},
		"every assignment": {`func F(c bool) Event {
	kind := "thing.a"
	if c {
		kind = "thing.b"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.b v1"}, nil},
		"closure assignment": {`func F() Event {
	kind := "thing.a"
	defer func() { kind = "thing.b" }()
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.b v1"}, nil},
		"self assignment": {`func F(n int) Event {
	kind := "thing.a"
	for range n {
		kind = kind
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}, nil},
		"same-type conversion": {`func F() Event {
	kind := SubjectProduct
	return Event{Kind: string(kind), PayloadVersion: 1}
}`, []string{"product v1"}, nil},
		// Each field assignment overwrites the declaration's zero value.
		"field assignments": {`func F() Event {
	var event Event
	event.Kind = "thing.b"
	event.PayloadVersion = 2
	return event
}`, []string{"thing.b v2"}, nil},
		"zero version": {`func F() Event { return Event{Kind: "thing.b", PayloadVersion: 0} }`, []string{"thing.b v0"}, nil},
		"copied event": {`func F() Event {
	base := Event{Kind: "thing.b", PayloadVersion: 2}
	next := base
	next.PayloadVersion = 3
	return next
}`, []string{"thing.b v2", "thing.b v3"}, nil},
		"rune conversion": {`func F() Event {
	n := 65
	return Event{Kind: string(rune(n)), PayloadVersion: 1}
}`, nil, []string{"* v1"}},
		"narrowing conversion": {`func F() Event {
	n := 300
	return Event{Kind: "thing.a", PayloadVersion: int(int8(n))}
}`, nil, []string{"thing.a v*"}},
		"escaped address": {`func F() Event {
	kind := "thing.a"
	set(&kind)
	return Event{Kind: kind, PayloadVersion: 1}
}

func set(kind *string) { *kind = "thing.c" }`, nil, []string{"* v1"}},
		"pointer method": {`func (e *Event) bump() { e.PayloadVersion++ }

func F() Event {
	event := Event{Kind: "thing.a", PayloadVersion: 1}
	event.bump()
	event.Kind = "thing.b"
	return event
}`, []string{"thing.a v1"}, []string{"thing.b v*"}},
		"op-assignment": {`func F() Event {
	version := 1
	version += 1
	return Event{Kind: "thing.a", PayloadVersion: version}
}`, nil, []string{"thing.a v*"}},
		"increment": {`func F() Event {
	version := 1
	version++
	return Event{Kind: "thing.a", PayloadVersion: version}
}`, nil, []string{"thing.a v*"}},
		"concatenation in a loop": {`func F(n int) Event {
	kind := "thing.a"
	for range n {
		kind = kind + ".x"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, nil, []string{"* v1"}},
		"tuple declaration": {`func F() Event {
	var event, err = pairEvent()
	_ = err
	event.Kind = "thing.b"
	return event
}

func pairEvent() (Event, error) { return Event{}, nil }`, []string{"thing.b v0"}, nil},
		"embedded event": {`type wrapped struct{ Event }

func F() Event {
	var w wrapped
	w.Event = Event{Kind: "thing.a", PayloadVersion: 7}
	w.Kind = "thing.b"
	return w.Event
}`, []string{"thing.a v7"}, []string{"thing.b v*"}},
		"tuple result": {`func F() Event {
	kind, version := pair()
	return Event{Kind: kind, PayloadVersion: version}
}

func pair() (string, int) { return "thing.a", 1 }`, []string{"thing.a v1"}, nil},
		"spread tuple result": {`func F() Event {
	kind, version := via()
	return Event{Kind: kind, PayloadVersion: version}
}

func via() (string, int) { return pair() }

func pair() (string, int) { return "thing.a", 1 }`, []string{"thing.a v1"}, nil},
		"bare return": {`func F() Event { return Event{Kind: namedKind(), PayloadVersion: 1} }

func namedKind() (kind string) {
	kind = "thing.a"
	return
}`, nil, []string{"* v1"}},
		"tuple field target": {`func F() Event {
	var event Event
	event.Kind, event.PayloadVersion = pair()
	return Event{Kind: event.Kind, PayloadVersion: 1}
}

func pair() (string, int) { return "thing.a", 1 }`, []string{" v1", "thing.a v1"}, []string{"* v*"}},
		"each result of one call": {`func F(n int) []Event {
	kind := "thing.a"
	var out []Event
	for range n {
		var next string
		next, kind = pass(kind)
		out = append(out, Event{Kind: next, PayloadVersion: 1})
	}
	return out
}

func pass(kind string) (string, string) { return kind, "thing.c" }`, []string{"thing.a v1", "thing.c v1"}, nil},
		"range value": {`func F(kinds []string) Event {
	kind := "thing.a"
	for _, kind = range kinds {
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, nil, []string{"* v1"}},
		"global": {`var defaultKind = "thing.a"

func F() Event { return Event{Kind: defaultKind, PayloadVersion: 1} }`, nil, []string{"* v1"}},
		"parameter field": {`func F(event Event) Event {
	event.Kind = "thing.b"
	return event
}`, nil, []string{"thing.b v*"}},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixture(t, mutation{"internal/store/store.go", anchor, c.code + "\n\n" + anchor})
			writes := slices.DeleteFunc(slices.Clone(analysis.Writes), func(pair string) bool { return pair == "thing.a v2" })
			var open []string
			for _, s := range analysis.Open {
				if s.Function == storePkg+".F" {
					open = append(open, s.Pairs...)
				}
			}
			if !slices.Equal(writes, c.writes) || !slices.Equal(open, c.open) {
				t.Fatalf("writes %q, open %q", writes, open)
			}
		})
	}
}

// An assignment in a constantly skipped arm, or overwritten before any read,
// supplies no value; anything that could carry it past the overwrite keeps it.
func TestSitePairsPrune(t *testing.T) {
	const anchor = "func ReadEvent("
	for name, c := range map[string]struct {
		code   string
		writes []string
	}{
		"constant false arm": {`const debug = false

func F() Event {
	kind := "thing.a"
	if debug {
		kind = "thing.c"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}},
		"constant true condition skips else": {`const live = true

func F() Event {
	kind := "thing.a"
	if live {
	} else {
		kind = "thing.c"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}},
		"local comparison": {`func F() Event {
	mode, kind := "x", "thing.a"
	if mode == "y" || mode != "x" && true {
		kind = "thing.c"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}},
		"interface comparison": {`func F() Event {
	var mode any = int64(1)
	kind := "thing.a"
	if mode != 1 {
		kind = "thing.c"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.c v1"}},
		"read in an if init": {`func F() Event {
	kind := "thing.c"
	if first := kind; true {
		kind = "thing.a"
		return Event{Kind: first, PayloadVersion: 1}
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.c v1"}},
		"overwritten": {`func F() Event {
	kind := "thing.c"
	kind = "thing.a"
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}},
		"overwritten in a constant true arm": {`func F() Event {
	kind := "thing.c"
	if true {
		kind = "thing.a"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1"}},
		"read before the overwrite": {`func F() Event {
	kind := "thing.c"
	first := Event{Kind: kind, PayloadVersion: 1}
	kind = "thing.a"
	_ = first
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.c v1"}},
		"captured by a closure": {`func F() Event {
	kind := "thing.b"
	f := func() Event { return Event{Kind: kind, PayloadVersion: 1} }
	kind = "thing.c"
	_ = f()
	kind = "thing.a"
	return f()
}`, []string{"thing.a v1", "thing.b v1", "thing.c v1"}},
		"break before the overwrite": {`func F(n int) Event {
	kind := "thing.a"
	for range n {
		kind = "thing.c"
		if n > 1 {
			break
		}
		kind = "thing.a"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.c v1"}},
		"goto past the overwrite": {`func F(n int) Event {
	kind := "thing.c"
	if n > 0 {
		goto done
	}
	kind = "thing.a"
done:
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.a v1", "thing.c v1"}},
		"condition through a cycle": {`func F() Event {
	kind, other := "thing.r", "thing.q"
	for range 3 {
		if other == "thing.r" {
			kind = "thing.s"
		}
		other = kind
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, []string{"thing.r v1", "thing.s v1"}},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixture(t, mutation{"internal/store/store.go", anchor, c.code + "\n\n" + anchor})
			writes := slices.DeleteFunc(slices.Clone(analysis.Writes), func(pair string) bool { return pair == "thing.a v2" })
			for _, s := range analysis.Open {
				if s.Function == storePkg+".F" {
					t.Fatalf("open site %+v", s)
				}
			}
			if !slices.Equal(writes, c.writes) {
				t.Fatalf("writes %q", writes)
			}
		})
	}
}

// A parameter resolves through its function's static calls, one site
// evaluation per call; a call result resolves through the callee's returns.
// A function whose calls are not all visible keeps its parameters open.
func TestSitePairsThroughCalls(t *testing.T) {
	const anchor = "func ReadEvent("
	base := analyzeFixture(t)
	known := map[string]bool{}
	for _, s := range base.Open {
		known[s.Function+" "+s.Expression] = true
	}
	for name, c := range map[string]struct {
		code   string
		writes []string
		open   []string
	}{
		"parameter": {`func F(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

func G() { F("thing.a"); F("thing.b") }`, []string{"thing.a v1", "thing.b v1"}, nil},
		"one evaluation per call": {`func F(kind string, version int) Event { return Event{Kind: kind, PayloadVersion: version} }

func G() { F("thing.a", 1); F("thing.b", 2) }`, []string{"thing.a v1", "thing.b v2"}, nil},
		"through two calls": {`func F(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

func G(kind string) Event { return F(kind) }

func H() { G("thing.a"); F("thing.b") }`, []string{"thing.a v1", "thing.b v1"}, nil},
		"call result": {`func kindOf() string { return "thing.c" }

func F() Event { return Event{Kind: kindOf(), PayloadVersion: 1} }`, []string{"thing.c v1"}, nil},
		"call result with bound arms": {`func pick(first bool) string {
	if first {
		return "thing.a"
	} else {
		return "thing.b"
	}
}

func F() Event { return Event{Kind: pick(true), PayloadVersion: 1} }`, []string{"thing.a v1"}, nil},
		"recursion": {`func F(kind string, n int) Event {
	if n > 0 {
		return F(kind, n-1)
	}
	return Event{Kind: kind, PayloadVersion: 1}
}

func G() { F("thing.a", 3) }`, []string{"thing.a v1"}, nil},
		"unexported method": {`type maker struct{}

func (maker) make(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

func G() { maker{}.make("thing.a") }`, []string{"thing.a v1"}, nil},
		"parameter in a condition": {`func F(mode string) Event {
	kind := "thing.a"
	if mode == "y" {
		kind = "thing.c"
	}
	return Event{Kind: kind, PayloadVersion: 1}
}

func G() { F("y") }`, []string{"thing.a v1", "thing.c v1"}, nil},
		"no call": {`func F(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }`, nil, []string{"* v1"}},
		"assigned parameter": {`func F(kind string) Event {
	kind = "thing.c"
	return Event{Kind: kind, PayloadVersion: 1}
}

func G() { F("thing.a") }`, []string{"thing.a v1", "thing.c v1"}, nil},
		"parameter field": {`func F(event Event) Event {
	event.PayloadVersion = 4
	return event
}

func G() { F(Event{Kind: "thing.b", PayloadVersion: 3}) }`, []string{"thing.b v3", "thing.b v4"}, nil},
		"function value": {`func F(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

var hook = F

func G() { F("thing.a") }`, nil, []string{"* v1"}},
		"exported method": {`type Maker struct{}

func (Maker) Make(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

func G() { Maker{}.Make("thing.a") }`, nil, []string{"* v1"}},
		"method an interface call names": {`type maker struct{}

func (maker) make(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

type making interface{ make(string) Event }

func G(m making) { maker{}.make("thing.a"); m.make("thing.b") }`, nil, []string{"* v1"}},
		"method expression": {`type maker struct{}

func (maker) make(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

func G() { maker{}.make("thing.a"); maker.make(maker{}, "thing.b") }`, nil, []string{"* v1"}},
		"interface method value": {`type maker struct{}

func (maker) make(kind string) Event { return Event{Kind: kind, PayloadVersion: 1} }

type making interface{ make(string) Event }

func G(m making) { maker{}.make("thing.a"); h := m.make; h("thing.b") }`, nil, []string{"* v1"}},
		"dead recursion": {`func F(kind string, n int) Event {
	if n > 0 {
		return F(kind, n-1)
	}
	return Event{Kind: kind, PayloadVersion: 1}
}`, nil, []string{"* v*"}},
		"named result and defer": {`func kindOf() (kind string) {
	defer func() { kind = "thing.c" }()
	return "thing.a"
}

func F() Event { return Event{Kind: kindOf(), PayloadVersion: 1} }`, nil, []string{"* v1"}},
		"unnamed result and recover": {`func kindOf(fail bool) string {
	defer func() { _ = recover() }()
	if fail {
		panic("fail")
	}
	return "thing.a"
}

func F() Event { return Event{Kind: kindOf(true), PayloadVersion: 1} }`, nil, []string{"* v1"}},
		"method expression result": {`type namer string

func (namer) name(kind string) string { return kind }

func F() Event { return Event{Kind: namer.name("thing.a", "thing.c"), PayloadVersion: 1} }`, nil, []string{"* v1"}},
		"interface method result": {`type namer interface{ kind() string }

func F(n namer) Event { return Event{Kind: n.kind(), PayloadVersion: 1} }`, nil, []string{"* v1"}},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixture(t, mutation{"internal/store/store.go", anchor, c.code + "\n\n" + anchor})
			writes := slices.DeleteFunc(slices.Clone(analysis.Writes), func(pair string) bool { return pair == "thing.a v2" })
			var open []string
			for _, s := range analysis.Open {
				if !known[s.Function+" "+s.Expression] {
					open = append(open, s.Pairs...)
				}
			}
			if !slices.Equal(writes, c.writes) || !slices.Equal(open, c.open) {
				t.Fatalf("writes %q, open %q", writes, open)
			}
		})
	}
}

// A registry read resolves to the kind's registered current version only
// while the binary never writes the registry or the read value, and that
// version is never compared.
func TestSitePairsThroughRegistry(t *testing.T) {
	const anchor = "func ReadEvent("
	const typed = `func registered(kind string) (EventKindRegistration, bool) {
	registration, ok := eventKindRegistry[kind]
	return registration, ok
}

func F(kind string) Event {
	version := 1
	if registration, ok := registered(kind); ok {
		version = registration.CurrentVersion
	}
	return Event{Kind: kind, PayloadVersion: version}
}

func G() { F("thing.a"); F("thing.z") }`
	resolved := []string{"thing.a vcur:thing.a", "thing.z v1", "thing.z vcur:thing.z"}
	base := analyzeFixture(t)
	known := map[string]bool{}
	for _, s := range base.Open {
		known[s.Function+" "+s.Expression] = true
	}
	for name, c := range map[string]struct {
		code   string
		extra  []mutation
		writes []string
		open   []string
	}{
		"current version": {typed, nil, resolved, nil},
		"spread result": {strings.Replace(typed, "registered(kind); ok", "via(kind); ok", 1) + `

func via(kind string) (EventKindRegistration, bool) { return registered(kind) }`, nil, resolved, nil},
		"registry written":      {typed + "\n\nfunc H() { eventKindRegistry[\"thing.a\"] = EventKindRegistration{CurrentVersion: 5} }", nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"registry deleted":      {typed + "\n\nfunc H() { delete(eventKindRegistry, \"thing.a\") }", nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"registry reassigned":   {typed + "\n\nfunc H() { eventKindRegistry = nil }", nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"registry range target": {typed + "\n\nfunc H(k string) {\n\tfor _, eventKindRegistry[k] = range []EventKindRegistration{{}} {\n\t}\n}", nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"registry from a call":  {typed, []mutation{{"internal/store/store.go", "var eventKindRegistry = map[string]EventKindRegistration{", "var eventKindRegistry = registryOf(map[string]EventKindRegistration{"}, {"internal/store/store.go", "Fold: foldB},\n}", "Fold: foldB},\n})\n\nfunc registryOf(m map[string]EventKindRegistration) map[string]EventKindRegistration { return m }"}}, nil, []string{"thing.a v*", "thing.z v*"}},
		"read value written":    {strings.Replace(typed, "\t\tversion = registration.CurrentVersion", "\t\tregistration.CurrentVersion = 9\n\t\tversion = registration.CurrentVersion", 1), nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"read value address": {strings.Replace(typed, "\t\tversion = registration.CurrentVersion", "\t\tbump(&registration)\n\t\tversion = registration.CurrentVersion", 1) + `

func bump(r *EventKindRegistration) { r.CurrentVersion = 9 }`, nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"registry parenthesized target": {typed + "\n\nfunc H() { (eventKindRegistry[\"thing.a\"]) = EventKindRegistration{CurrentVersion: 5} }", nil, nil, []string{"thing.a v*", "thing.z v*"}},
		"converted copy written": {`type registrationCopy EventKindRegistration

func F() Event {
	changed := registrationCopy(eventKindRegistry["thing.a"])
	changed.CurrentVersion = 9
	return Event{Kind: "thing.a", PayloadVersion: EventKindRegistration(changed).CurrentVersion}
}`, nil, nil, []string{"thing.a v*"}},
		"current version compared": {`func F() Event {
	version := 1
	if eventKindRegistry["thing.a"].CurrentVersion == 2 {
		version = 3
	}
	return Event{Kind: "thing.a", PayloadVersion: version}
}`, nil, []string{"thing.a v1", "thing.a v3"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixture(t, append([]mutation{{"internal/store/store.go", anchor, c.code + "\n\n" + anchor}}, c.extra...)...)
			writes := slices.DeleteFunc(slices.Clone(analysis.Writes), func(pair string) bool { return slices.Contains(base.Writes, pair) })
			var open []string
			for _, s := range analysis.Open {
				if !known[s.Function+" "+s.Expression] {
					open = append(open, s.Pairs...)
				}
			}
			if !slices.Equal(writes, c.writes) || !slices.Equal(open, c.open) {
				t.Fatalf("writes %q, open %q", writes, open)
			}
		})
	}
}

// A holder reaches an interface or unsafe pointer through a variadic
// argument, a slice of holders, or a pointer conversion.
func TestHolderEscapesAreSites(t *testing.T) {
	const anchor = "func ReadEvent("
	analysis := analyzeFixture(t, mutation{"internal/store/store.go", "import (", "import (\n\t\"fmt\"\n\t\"unsafe\""},
		mutation{"internal/store/store.go", anchor, `func Print(event *Event) string { return fmt.Sprint("event", event) }

func Batch(events []*Event) string { return fmt.Sprint(events) }

func Raw(event *Event) unsafe.Pointer { return unsafe.Pointer(event) }

` + anchor})
	site(t, analysis, storePkg+".Print", `fmt.Sprint("event", event)`)
	site(t, analysis, storePkg+".Batch", "fmt.Sprint(events)")
	site(t, analysis, storePkg+".Raw", "unsafe.Pointer(event)")
}

// A type parameter holds an Event only through an instantiation the binary
// makes, including one through another generic.
func TestTypeParameterHoldsWhatItIsInstantiatedWith(t *testing.T) {
	const anchor = "func ReadEvent("
	generic := `func decode[T any](data []byte, value *T) error { return json.Unmarshal(data, value) }

func relay[T any](data []byte, value *T) error { return decode(data, value) }

type box[T any] struct{ value *T }

func (b box[T]) fill(data []byte) error { return json.Unmarshal(data, b.value) }

`
	payload := analyzeFixture(t, mutation{"internal/store/store.go", anchor, generic + `func LoadPayload(data []byte) (map[string]int, error) {
	var payload map[string]int
	err := relay(data, &payload)
	return payload, errors.Join(err, box[map[string]int]{&payload}.fill(data))
}

` + anchor})
	for _, s := range payload.Open {
		if strings.HasSuffix(s.Function, ".decode") || strings.HasSuffix(s.Function, ".fill") {
			t.Fatalf("a parameter never bound to an Event holds one: %+v", s)
		}
	}
	event := analyzeFixture(t, mutation{"internal/store/store.go", anchor, generic + `func LoadEvent(data []byte) (Event, error) {
	var event Event
	err := relay(data, &event)
	return event, errors.Join(err, box[Event]{&event}.fill(data))
}

` + anchor})
	site(t, event, storePkg+".decode", "json.Unmarshal(data, value)")
	site(t, event, "("+storePkg+".box[T]).fill", "json.Unmarshal(data, b.value)")
}

// An instantiation in another package binds the declaring package's parameter.
func TestTypeParameterBoundInAnotherPackage(t *testing.T) {
	analysis := analyzeFixture(t,
		mutation{"internal/store/store.go", "func ReadEvent(", "func Decode[T any](data []byte, value *T) error { return json.Unmarshal(data, value) }\n\nfunc ReadEvent("},
		mutation{"cmd/concord/main.go", "func keep(store.Event) {}", "func keep(event store.Event) { _ = store.Decode(nil, &event) }"})
	site(t, analysis, storePkg+".Decode", "json.Unmarshal(data, value)")
}

func TestEmbeddedSQLIsASite(t *testing.T) {
	analysis := analyzeFixture(t, mutation{"internal/store/store.go", "import (", "import (\n\t_ \"embed\""},
		mutation{"internal/store/store.go", "type Tx struct{}", "//go:embed rewrite.sql\nvar rewrite string\n\ntype Tx struct{}"},
		mutation{"internal/store/rewrite.sql", "", "UPDATE domain_events SET kind = 'thing.b';\n"})
	site(t, analysis, storePkg+" embed rewrite.sql", "embedded file writes domain_events")
}

func TestUnanalyzableSourceIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		mutation
		refusal string
	}{
		"no store Event":  {mutation{"cmd/concord/main.go", fixtureMain, "package main\n\nfunc main() {}\n"}, "does not compile internal/store.Event"},
		"type error":      {mutation{"internal/store/store.go", "func ReadEvent(", "var broken int = \"x\"\n\nfunc ReadEvent("}, "cannot use"},
		"package outside": {mutation{"cmd/concord/main.go", `"example.com/fixture/internal/store"`, "\"example.com/fixture/internal/store\"\n\t_ \"example.com/fixture/lib\""}, "lies outside internal/ and cmd/"},
	} {
		t.Run(name, func(t *testing.T) {
			analysis := analyzeFixtureRefusal(t, c.mutation, mutation{"lib/lib.go", "", "package lib\n"})
			if !strings.Contains(analysis.Refused, c.refusal) || len(analysis.Open) != 0 {
				t.Fatalf("analysis = %+v", analysis)
			}
		})
	}
}

// A field promoted through embedded values and pointers is still the Event's
// field; a field that shadows it is not.
func TestPromotedEventFieldIsASite(t *testing.T) {
	const anchor = "func ReadEvent("
	code := "type inner struct{ Event }\n\ntype outer struct{ *inner }\n\ntype shadow struct {\n\tEvent\n\tKind string\n}\n\n" +
		"func Promote(o *outer) { o.Kind = \"thing.a\" }\n\nfunc Shadow(s *shadow) { s.Kind = \"thing.a\" }\n\n"
	analysis := analyzeFixture(t, mutation{"internal/store/store.go", anchor, code + anchor})
	site(t, analysis, storePkg+".Promote", `o.Kind = "thing.a"`)
	for _, s := range analysis.Open {
		if s.Function == storePkg+".Shadow" {
			t.Errorf("a shadowing field is a site: %+v", s)
		}
	}
}
