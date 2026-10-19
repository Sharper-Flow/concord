// Command release-restore-inventory records each release tag's event registry
// and payload shapes: it parses the registry literal, then compiles a reflection
// probe against that tag's own source. Tags with identical internal trees,
// go.mod, and go.sum share one probe. An unrecognized
// registry entry refuses the tag. Each written (kind, version, type) is then
// joined to the witness logs: historical corpus rows, and writer-produced rows
// from scripts/release-restore-gate.py, each bound to its own release's
// registered shape. An unwitnessed shape, a writer row that binds to no
// registered shape of its release, or an unapproved refusal holds (exit 1).
// Run from the repository root.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	schemaID = "concord.release-restore-writers.v1"
	workers  = 4
	// commandTimeout bounds one git, tar, or probe compile-and-run step.
	commandTimeout = 10 * time.Minute
	// A recorded ruling accepts form A (v0.1.0..v0.37.1) as a named exclusion.
	untypedReason = "registry entries name no payload type (form A); accepted named exclusion from shape coverage"
)

var typedConstructors = map[string]bool{"registerEventKind": true, "registerScopedEventKind": true, "workflowRegistration": true, "workflowScopedRegistration": true}

//go:embed release-restore-inventory.go
var generatorSource string

const excludedFormA = ` v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.6.0 v0.7.0 v0.7.1 v0.8.0 v0.9.0 v0.10.0 v0.10.1 v0.11.0 v0.12.0 v0.13.0 v0.14.0 v0.15.0 v0.15.1 v0.15.2 v0.15.3 v0.15.4 v0.16.0 v0.16.1 v0.16.2 v0.17.0 v0.17.1 v0.17.2 v0.18.0 v0.19.0 v0.20.0 v0.21.0 v0.22.0 v0.23.0 v0.24.0 v0.25.0 v0.26.0 v0.26.1 v0.27.0 v0.27.1 v0.28.0 v0.29.0 v0.30.0 v0.31.0 v0.32.0 v0.33.0 v0.33.1 v0.33.2 v0.33.3 v0.33.4 v0.34.0 v0.35.0 v0.36.0 v0.37.0 v0.37.1 `

var strictTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type row struct {
	Tag       string `json:"tag"`
	TagObject string `json:"tag_object"`
	Commit    string `json:"commit"`
	Group     string `json:"group,omitempty"`
	Status    string `json:"status"`
	Excluded  bool   `json:"excluded,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Registry  string `json:"registry_source,omitempty"`
}

type registration struct {
	Kind            string `json:"kind"`
	PayloadType     string `json:"payload_type"`
	CurrentVersion  int    `json:"current_version"`
	MinSupported    int    `json:"min_supported"`
	Upcasters       []int  `json:"upcasters"`
	RecordedSchemas []int  `json:"recorded_schemas"`
	Type            string `json:"type"`
}

type inventory struct {
	Schema    string                     `json:"schema"`
	Generator string                     `json:"generator"`
	Rows      []row                      `json:"rows"`
	Groups    map[string][]registration  `json:"groups"`
	Types     map[string]json.RawMessage `json:"types"`
}

// command runs a fixed program in dir and returns its standard output.
func command(dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is a fixed program, argv values stay separate, and no shell runs.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly -trimpath -p=2", "CGO_ENABLED=0", "GOMAXPROCS=2")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, stderr.Bytes()[max(0, stderr.Len()-2000):])
	}
	return out, nil
}

func git(args ...string) (string, error) {
	out, err := command("", "git", args...)
	return strings.TrimSpace(string(out)), err
}

// registryEntries parses one registry source into the probe's type table. Each
// key must be a string literal or a constant name the compiler resolves, each
// entry must name its payload type (see typedPayload); form A refuses.
func registryEntries(src []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "operation.go", src, 0)
	if err != nil {
		return nil, fmt.Errorf("registry source does not parse: %v", err)
	}
	var literal *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if ok && len(spec.Names) == 1 && spec.Names[0].Name == "eventKindRegistry" && len(spec.Values) == 1 {
			literal, _ = spec.Values[0].(*ast.CompositeLit)
		}
		return literal == nil
	})
	if literal == nil {
		return nil, errors.New("no eventKindRegistry composite literal")
	}
	entries := make([]string, 0, len(literal.Elts))
	untyped := 0
	for _, element := range literal.Elts {
		pair, _ := element.(*ast.KeyValueExpr)
		key := ""
		if pair != nil {
			key = name(pair.Key)
			if lit, ok := pair.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				key = lit.Value
			}
		}
		if key == "" {
			return nil, errors.New("registry entry without a literal or constant kind key")
		}
		if _, composite := pair.Value.(*ast.CompositeLit); composite {
			untyped++
			continue
		}
		payload := typedPayload(pair.Value)
		if payload == "" {
			return nil, fmt.Errorf("registry entry %s has an unrecognized form", key)
		}
		entries = append(entries, fmt.Sprintf("\t%s: reflect.TypeOf((*%s)(nil)).Elem(),\n", key, payload))
	}
	if untyped > 0 && untyped == len(literal.Elts) {
		return nil, errors.New(untypedReason)
	}
	if untyped > 0 || len(entries) == 0 {
		return nil, errors.New("empty or mixed typed/untyped registry")
	}
	return entries, nil
}

// typedPayload names T in one of the registration constructors generic over
// the payload type, optionally inside withRecordedSchemas.
func typedPayload(expr ast.Expr) string {
	call, _ := expr.(*ast.CallExpr)
	if call == nil {
		return ""
	}
	if name(call.Fun) == "withRecordedSchemas" && len(call.Args) == 2 {
		return typedPayload(call.Args[0])
	}
	index, _ := call.Fun.(*ast.IndexExpr)
	if index == nil || !typedConstructors[name(index.X)] {
		return ""
	}
	return name(index.Index)
}

func name(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// probeOutput is the probe's report; a kind it was not given prints as null.
type probeOutput struct {
	Registry map[string]*registration   `json:"registry"`
	Types    map[string]json.RawMessage `json:"types"`
}

// probe compiles and runs the shape probe against one tag's archived source.
func probe(commit string, entries []string) (probeOutput, error) {
	var out probeOutput
	dir, err := os.MkdirTemp("", "release-restore-probe-")
	if err != nil {
		return out, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tarball := filepath.Join(dir, "source.tar")
	if _, err := git("archive", "--format=tar", "-o", tarball, commit); err != nil {
		return out, err
	}
	if _, err := command(dir, "tar", "-xf", tarball); err != nil {
		return out, err
	}
	module, err := command(dir, "go", "list", "-m")
	if err != nil {
		return out, err
	}
	err = errors.Join(os.Mkdir(filepath.Join(dir, "releaserestoreprobe"), 0o750),
		os.WriteFile(filepath.Join(dir, "internal/store/zz_release_restore_probe.go"), []byte(strings.Replace(probeTemplate, "\t// TYPES\n", strings.Join(entries, ""), 1)), 0o600),
		os.WriteFile(filepath.Join(dir, "releaserestoreprobe/main.go"), []byte(strings.ReplaceAll(probeMain, "MODULE", strings.TrimSpace(string(module)))), 0o600))
	if err != nil {
		return out, err
	}
	report := filepath.Join(dir, "probe.json")
	if _, err := command(dir, "go", "run", "./releaserestoreprobe", report); err != nil {
		return out, fmt.Errorf("probe %s: %w", commit, err)
	}
	data, err := os.ReadFile(filepath.Clean(report))
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	return out, err
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// group reads one probe's registrations. The probe's registry must hold
// exactly the parsed entries, so a kind the parser lost or invented refuses.
func group(entries int, out probeOutput, types map[string]json.RawMessage) ([]registration, error) {
	if len(out.Registry) != entries {
		return nil, fmt.Errorf("probe registry has %d kinds, parsed %d", len(out.Registry), entries)
	}
	registrations := make([]registration, 0, entries)
	for kind, meta := range out.Registry {
		if meta == nil || out.Types[kind] == nil {
			return nil, fmt.Errorf("registry kind %q has no parsed payload type", kind)
		}
		meta.Kind, meta.Type = kind, digest(out.Types[kind])
		registrations = append(registrations, *meta)
	}
	for _, r := range registrations {
		types[r.Type] = out.Types[r.Kind]
	}
	sort.Slice(registrations, func(i, j int) bool { return registrations[i].Kind < registrations[j].Kind })
	return registrations, nil
}

func listTags() ([]row, error) {
	listing, err := git("for-each-ref", "--sort=v:refname", "--format=%(refname:strip=2) %(objectname) %(*objectname)", "refs/tags")
	if err != nil {
		return nil, err
	}
	var tags []row
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strictTag.MatchString(fields[0]) {
			continue
		}
		tags = append(tags, row{Tag: fields[0], TagObject: fields[1], Commit: fields[len(fields)-1], Status: "inventoried"})
	}
	return tags, nil
}

// inspect parses one tag's registry and names its probe group: the exact
// internal tree, go.mod, go.sum, and probe source.
func inspect(result row) (row, []string) {
	src, err := command("", "git", "show", result.Commit+":internal/store/operation.go")
	var entries []string
	if err == nil {
		result.Registry = digest(src)
		entries, err = registryEntries(src)
	}
	var inputs string
	if err == nil {
		inputs, err = git("rev-parse", result.Commit+":internal", result.Commit+":go.mod", result.Commit+":go.sum")
	}
	if err != nil {
		result.Status, result.Detail = "inventory_refused", err.Error()
		result.Excluded = err.Error() == untypedReason && strings.Contains(excludedFormA, " "+result.Tag+" ")
		return result, nil
	}
	result.Group = digest([]byte(inputs + "\n" + generatorSource))
	return result, entries
}

func main() {
	output := flag.String("out", "cmd/concord/testdata/release_restore/writers.json", "inventory to write")
	coverageOut := flag.String("coverage", "cmd/concord/testdata/release_restore/coverage.json", "shape coverage to write")
	var witnesses []string
	flag.Func("witnesses", "witness log (JSON lines) to bind shapes to: a historical corpus or release gate log; repeatable", func(path string) error {
		witnesses = append(witnesses, path)
		return nil
	})
	flag.Parse()
	if err := run(*output, *coverageOut, witnesses); err != nil {
		fmt.Fprintln(os.Stderr, "release-restore-inventory:", err)
		os.Exit(1)
	}
}

func run(output, coverageOut string, witnessLogs []string) error {
	if len(witnessLogs) == 0 {
		return errors.New("-witnesses is required: shapes without a witness join are not coverage")
	}
	var witnesses []witness
	for _, log := range witnessLogs {
		rows, err := readWitnesses(log)
		if err != nil {
			return err
		}
		witnesses = append(witnesses, rows...)
	}
	tags, err := listTags()
	if err != nil || len(tags) == 0 {
		return fmt.Errorf("release tags unavailable or empty: %v", err)
	}
	inv := inventory{Schema: schemaID, Generator: "go run scripts/release-restore-inventory.go", Groups: map[string][]registration{}, Types: map[string]json.RawMessage{}}
	type pending struct {
		commit  string
		entries []string
	}
	work := map[string]pending{}
	for _, ref := range tags {
		result, entries := inspect(ref)
		if result.Group != "" {
			work[result.Group] = pending{result.Commit, entries}
		}
		inv.Rows = append(inv.Rows, result)
	}
	failures := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	next := make(chan string)
	for range workers {
		wg.Go(func() {
			for key := range next {
				out, err := probe(work[key].commit, work[key].entries)
				mu.Lock()
				if err == nil {
					inv.Groups[key], err = group(len(work[key].entries), out, inv.Types)
				}
				if err != nil {
					delete(inv.Groups, key)
					failures[key] = err
				}
				mu.Unlock()
			}
		})
	}
	for key := range work {
		next <- key
	}
	close(next)
	wg.Wait()
	for i, result := range inv.Rows {
		if err := failures[result.Group]; err != nil {
			inv.Rows[i].Status, inv.Rows[i].Detail = "inventory_refused", err.Error()
		}
	}
	data, err := json.MarshalIndent(inv, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(data, '\n'), 0o600); err != nil {
		return err
	}
	coverage, unbound, err := cover(inv, witnesses)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(map[string]any{"schema": "concord.release-restore-coverage.v1", "shapes": coverage, "unbound_writer_witnesses": unbound}, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(coverageOut, append(data, '\n'), 0o600); err != nil {
		return err
	}
	var held []error
	for _, result := range inv.Rows {
		if result.Status == "inventory_refused" && !result.Excluded {
			held = append(held, errors.New("inventory held: unapproved writer refusal; see output"))
			break
		}
	}
	if len(unbound) > 0 {
		held = append(held, errors.New("coverage held: a writer-produced row binds to no registered shape of its release; see coverage"))
	}
	for _, shape := range coverage {
		if !shape.Covered {
			held = append(held, errors.New("coverage held: a written shape has no complete witness; see coverage"))
			break
		}
	}
	return errors.Join(held...)
}

// witness is one admitted row: a passing historical corpus row
// (recordHistoricalWitness) names its Test; a writer-produced row from the
// release gate carries the Writer that produced it and its place in that
// writer's snapshot.
type witness struct {
	Test           string        `json:"test"`
	Writer         *writerSource `json:"writer"`
	Seq            int64         `json:"seq"`
	EventID        string        `json:"event_id"`
	Kind           string        `json:"kind"`
	PayloadVersion int           `json:"payload_version"`
	Payload        string        `json:"payload"`
}

// writerSource is the provenance chain of a writer-produced row: the release,
// its verified asset, the closed recipe it ran, and the snapshot it was read from.
type writerSource struct {
	Tag            string `json:"tag"`
	AssetDigest    string `json:"asset_digest"`
	Recipe         string `json:"recipe"`
	RecipeSHA256   string `json:"recipe_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

var (
	sha256Prefixed = regexp.MustCompile(`\Asha256:[0-9a-f]{64}\z`)
	sha256Bare     = regexp.MustCompile(`\A[0-9a-f]{64}\z`)
)

// complete reports whether a row carries every link of its provenance.
func (w witness) complete() bool {
	if w.Kind == "" || w.PayloadVersion < 1 {
		return false
	}
	if w.Writer == nil {
		return w.Test != ""
	}
	src := w.Writer
	return w.Test == "" && w.Seq > 0 && w.EventID != "" && src.Tag != "" && src.Recipe != "" &&
		sha256Prefixed.MatchString(src.AssetDigest) && sha256Bare.MatchString(src.RecipeSHA256) && sha256Bare.MatchString(src.SnapshotSHA256)
}

// name is the witness's identity in coverage output.
func (w witness) name() string {
	if w.Writer == nil {
		return w.Test
	}
	return "release:" + w.Writer.Tag + "@" + w.Writer.Recipe
}

// shapeCoverage binds one written (kind, version, type) to the witnesses whose
// payload is exactly that encoding. A field path no witness presents non-null
// stays missing, and the shape stays uncovered.
type shapeCoverage struct {
	Kind           string   `json:"kind"`
	PayloadVersion int      `json:"payload_version"`
	Type           string   `json:"type"`
	Writers        []string `json:"writers"`
	Witnesses      []string `json:"witnesses"`
	Missing        []string `json:"missing_paths,omitempty"`
	Covered        bool     `json:"covered"`
}

// readWitnesses loads one witness log before any probe runs.
func readWitnesses(witnessLog string) ([]witness, error) {
	data, err := os.ReadFile(filepath.Clean(witnessLog))
	if err != nil {
		return nil, fmt.Errorf("witness log: %w", err)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("witness log is empty: %s recorded no rows", witnessLog)
	}
	var rows []witness
	for _, line := range bytes.Split(data, []byte("\n")) {
		var w witness
		if err := json.Unmarshal(line, &w); err != nil || !w.complete() {
			return nil, fmt.Errorf("witness log line %q is not a witness row", line)
		}
		rows = append(rows, w)
	}
	return rows, nil
}

// cover joins witnesses to written shapes. A corpus row covers every shape of
// its (kind, version) it conforms to. A writer row covers only the shape its own
// release registers for its (kind, version), and only when it conforms to it;
// a writer row from an approved excluded release is excluded with it; any
// other writer row is returned as unbound.
func cover(inv inventory, rows []witness) ([]shapeCoverage, []string, error) {
	shapes := map[string]*shapeCoverage{}
	registered, excluded := map[string]string{}, map[string]bool{}
	for _, result := range inv.Rows {
		excluded[result.Tag] = result.Excluded
		if result.Status != "inventoried" {
			continue
		}
		for _, r := range inv.Groups[result.Group] {
			registered[fmt.Sprintf("%s\x00%s\x00%d", result.Tag, r.Kind, r.CurrentVersion)] = r.Type
			key := fmt.Sprintf("%s\x00%d\x00%s", r.Kind, r.CurrentVersion, r.Type)
			if shapes[key] == nil {
				shapes[key] = &shapeCoverage{Kind: r.Kind, PayloadVersion: r.CurrentVersion, Type: r.Type, Witnesses: []string{}}
			}
			shapes[key].Writers = append(shapes[key].Writers, result.Tag)
		}
	}
	unbound := []string{}
	bound := map[int]bool{}
	coverage := make([]shapeCoverage, 0, len(shapes))
	for _, s := range shapes {
		var shape any
		if err := json.Unmarshal(inv.Types[s.Type], &shape); err != nil {
			return nil, nil, fmt.Errorf("type %s: %w", s.Type, err)
		}
		missing, tests := map[string]bool{}, map[string]bool{}
		paths(shape, "$", missing)
		for i, w := range rows {
			var value any
			present := map[string]bool{}
			if w.Writer != nil && registered[fmt.Sprintf("%s\x00%s\x00%d", w.Writer.Tag, w.Kind, w.PayloadVersion)] != s.Type {
				continue
			}
			if w.Kind != s.Kind || w.PayloadVersion != s.PayloadVersion || json.Unmarshal([]byte(w.Payload), &value) != nil || !conforms(shape, value, "$", present) {
				continue
			}
			bound[i] = true
			tests[w.name()] = true
			for path := range present {
				delete(missing, path)
			}
		}
		for test := range tests {
			s.Witnesses = append(s.Witnesses, test)
		}
		for path := range missing {
			s.Missing = append(s.Missing, path)
		}
		sort.Strings(s.Witnesses)
		sort.Strings(s.Missing)
		s.Covered = len(tests) > 0 && len(missing) == 0
		coverage = append(coverage, *s)
	}
	for i, w := range rows {
		if w.Writer != nil && !bound[i] && !excluded[w.Writer.Tag] {
			unbound = append(unbound, fmt.Sprintf("%s seq %d %s v%d", w.Writer.Tag, w.Seq, w.Kind, w.PayloadVersion))
		}
	}
	sort.Strings(unbound)
	sort.Slice(coverage, func(i, j int) bool {
		a, b := coverage[i], coverage[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.PayloadVersion != b.PayloadVersion {
			return a.PayloadVersion < b.PayloadVersion
		}
		return a.Type < b.Type
	})
	return coverage, unbound, nil
}

// paths lists every JSON path a shape can present: "[]" steps into elements,
// "{}" into map values. Custom and recursive shapes end their path.
func paths(shape any, path string, out map[string]bool) {
	s, _ := shape.(map[string]any)
	out[path] = true
	switch s["kind"] {
	case "pointer":
		paths(s["elem"], path, out)
	case "slice", "array":
		paths(s["elem"], path+"[]", out)
	case "map":
		paths(s["elem"], path+"{}", out)
	case "object":
		fields, _ := s["fields"].([]any)
		for _, f := range fields {
			field, _ := f.(map[string]any)
			paths(field["shape"], path+"."+fmt.Sprint(field["name"]), out)
		}
	}
}

// conforms reports whether a decoded payload is exactly what encoding/json
// writes for the shape: every member it always emits is present, no member is
// unknown, and each value has the member's JSON type. It marks the paths the
// payload presents non-null.
func conforms(shape, value any, path string, present map[string]bool) bool {
	s, _ := shape.(map[string]any)
	kind, _ := s["kind"].(string)
	if value == nil {
		return kind == "pointer" || kind == "slice" || kind == "map" || kind == "bytes" || kind == "custom" || kind == "recursive"
	}
	present[path] = true
	switch kind {
	case "custom", "recursive":
		return true
	case "pointer":
		return conforms(s["elem"], value, path, present)
	case "string", "bytes":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		_, ok := value.(float64)
		return ok
	case "slice", "array":
		items, ok := value.([]any)
		if !ok || (kind == "array" && float64(len(items)) != s["length"]) {
			return false
		}
		for _, item := range items {
			if !conforms(s["elem"], item, path+"[]", present) {
				return false
			}
		}
		return true
	case "map":
		entries, ok := value.(map[string]any)
		for _, entry := range entries {
			ok = ok && conforms(s["elem"], entry, path+"{}", present)
		}
		return ok
	case "object":
		object, ok := value.(map[string]any)
		fields, _ := s["fields"].([]any)
		known := map[string]bool{}
		for _, f := range fields {
			field, _ := f.(map[string]any)
			name := fmt.Sprint(field["name"])
			known[name] = true
			member, has := object[name]
			switch {
			case !has:
				ok = ok && (field["omitempty"] == true || field["omitzero"] == true)
			case field["string"] == true:
				_, quoted := member.(string)
				ok = ok && quoted
				present[path+"."+name] = true
			default:
				ok = ok && conforms(field["shape"], member, path+"."+name, present)
			}
		}
		for name := range object {
			ok = ok && known[name]
		}
		return ok
	}
	return false
}

// probeTemplate joins a scratch copy of one release's internal/store. It reflects
// each payload type as encoding/json sees it and reads registry metadata by field
// name, so it compiles against every typed form. A type with its own JSON or text
// encoding, an interface, or an ambiguous embedding is "custom", not inferred.
const probeTemplate = `package store

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

var releaseRestoreProbeTypes = map[string]reflect.Type{
	// TYPES
}

func ReleaseRestoreProbe() ([]byte, error) {
	registry, types := map[string]any{}, map[string]any{}
	for kind, registration := range eventKindRegistry {
		payload, ok := releaseRestoreProbeTypes[kind]
		if !ok {
			registry[kind] = nil
			continue
		}
		meta := map[string]any{"payload_type": payload.Name(), "current_version": registration.CurrentVersion, "min_supported": registration.MinSupported}
		// A release whose registration type has no such field records it as
		// null, distinct from a present, empty map.
		for name, field := range map[string]string{"upcasters": "Upcasters", "recorded_schemas": "RecordedSchemas"} {
			meta[name] = nil
			value := reflect.ValueOf(registration).FieldByName(field)
			if !value.IsValid() {
				continue
			}
			keys := []int{}
			for _, key := range value.MapKeys() {
				keys = append(keys, int(key.Int()))
			}
			sort.Ints(keys)
			meta[name] = keys
		}
		registry[kind], types[kind] = meta, releaseRestoreShape(payload, map[reflect.Type]bool{})
	}
	return json.Marshal(map[string]any{"registry": registry, "types": types})
}

func releaseRestoreShape(t reflect.Type, seen map[reflect.Type]bool) map[string]any {
	if seen[t] {
		return map[string]any{"kind": "recursive", "type": t.String()}
	}
	seen[t] = true
	defer delete(seen, t)
	custom := t.Kind() == reflect.Interface || t == reflect.TypeOf(json.Number(""))
	for _, method := range []string{"MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText"} {
		_, owns := reflect.PointerTo(t).MethodByName(method)
		custom = custom || owns
	}
	if custom {
		return map[string]any{"kind": "custom", "type": t.String()}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return map[string]any{"kind": "pointer", "elem": releaseRestoreShape(t.Elem(), seen)}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return map[string]any{"kind": "bytes"}
		}
		shape := map[string]any{"kind": t.Kind().String(), "elem": releaseRestoreShape(t.Elem(), seen)}
		if t.Kind() == reflect.Array {
			shape["length"] = t.Len()
		}
		return shape
	case reflect.Map:
		return map[string]any{"kind": "map", "key": releaseRestoreShape(t.Key(), seen), "elem": releaseRestoreShape(t.Elem(), seen)}
	case reflect.Struct:
		fields, names := []map[string]any{}, map[string]bool{}
		if !releaseRestoreFields(t, seen, &fields, names) {
			return map[string]any{"kind": "custom", "type": t.String(), "reason": "ambiguous or nullable embedded fields"}
		}
		return map[string]any{"kind": "object", "fields": fields}
	default:
		return map[string]any{"kind": t.Kind().String()}
	}
}

// releaseRestoreFields lists a struct's JSON members, promoting untagged embedded
// structs. A repeated name refuses rather than reproducing dominance rules.
func releaseRestoreFields(t reflect.Type, seen map[reflect.Type]bool, fields *[]map[string]any, names map[string]bool) bool {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if field.Anonymous && name == "" && embedded.Kind() == reflect.Struct {
			if field.Type.Kind() == reflect.Pointer || seen[embedded] {
				return false
			}
			seen[embedded] = true
			ok := releaseRestoreFields(embedded, seen, fields, names)
			delete(seen, embedded)
			if !ok {
				return false
			}
			continue
		}
		if !field.IsExported() && !(field.Anonymous && embedded.Kind() == reflect.Struct) {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if names[name] {
			return false
		}
		names[name] = true
		member := map[string]any{"name": name, "shape": releaseRestoreShape(field.Type, seen)}
		for _, option := range strings.Split(options, ",") {
			if option == "omitempty" || option == "omitzero" || option == "string" {
				member[option] = true
			}
		}
		*fields = append(*fields, member)
	}
	return true
}
`

const probeMain = `package main

import (
	"os"

	"MODULE/internal/store"
)

func main() {
	data, err := store.ReleaseRestoreProbe()
	if err == nil {
		err = os.WriteFile(os.Args[1], data, 0o644)
	}
	if err != nil {
		panic(err)
	}
}
`
