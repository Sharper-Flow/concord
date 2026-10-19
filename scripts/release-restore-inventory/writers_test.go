package main

import (
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
	registration, ok := eventKindRegistry[event.Kind]
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
		if !slices.Equal(s.Pairs, []string{"* v*"}) || s.FunctionDigest == "" {
			t.Errorf("site %+v", s)
		}
		got = append(got, strings.TrimPrefix(strings.TrimPrefix(s.Function, storePkg+"."), mainPkg+".")+": "+s.Expression)
	}
	want := []string{
		`reconstruct: requested.PayloadVersion = existing.PayloadVersion`,
		`ReadEvent: &event.Kind`,
		`ReadEvent: &event.PayloadVersion`,
		`RecordA: Event{Kind: "thing.a", SubjectType: SubjectProduct, PayloadVersion: 2, Payload: payload}`,
		`upcastA1: event.PayloadVersion = 2`,
		`upcastB1: event.PayloadVersion = 2`,
		`upcastB2: event.PayloadVersion = 3`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sites:\n%s", strings.Join(got, "\n"))
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
		"literal eliding &Event": {`func Elided() []*Event { return []*Event{{Kind: "thing.a", PayloadVersion: 1}} }`, "Elided", `{Kind: "thing.a", PayloadVersion: 1}`},
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
