// Command release-restore-inventory records each release tag's event registry,
// payload shapes, and event writers: it parses the registry literal, compiles a
// reflection probe against that tag's own source, and type-checks the binary's
// writer sites (writers.go). Tags with identical internal and cmd trees,
// go.mod, go.sum, toolchain, and generator share one group. An unrecognized
// registry entry refuses the tag. Each registered (kind, version) is then
// joined to the witness logs: historical corpus rows, and writer-produced rows
// from scripts/release-restore-gate.py, each bound to its own release's
// registered shape or older accepted version, beside each release's writer
// finding (findings.go). An unwitnessed shape or older version, a writer row
// that binds to no registered shape or accepted version of its release, or an
// unapproved refusal holds (exit 1).
//
// With -cache, each group's probe result and analysis are kept in that directory and -batch N
// computes at most N missing groups per run, so a census can run as several
// bounded runs. A run that leaves groups missing writes nothing and exits 3.
// A kept probe failure stays until its cache file is removed.
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
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	schemaID = "concord.release-restore-writers.v1"
	workers  = 1
	// commandTimeout bounds one git, tar, or probe compile-and-run step.
	commandTimeout = 10 * time.Minute
	// A recorded ruling accepts form A (v0.1.0..v0.37.1) as a named exclusion.
	untypedReason = "registry entries name no payload type (form A); accepted named exclusion from shape coverage"
)

var typedConstructors = map[string]bool{"registerEventKind": true, "registerScopedEventKind": true, "workflowRegistration": true, "workflowScopedRegistration": true}

// The group key pins the sources that decide a group's probe and analysis;
// findings.go, coverage.go, and the exemption table decide only findings and
// coverage.
//
//go:embed main.go
var mainSource string

//go:embed writers.go
var writersSource string

var generatorSource = mainSource + writersSource

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
	Analyses  map[string]writerAnalysis  `json:"analyses"`
	Types     map[string]json.RawMessage `json:"types"`
}

// command runs a fixed program in dir and returns its standard output.
func command(dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is a fixed program, argv values stay separate, and no shell runs.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly -trimpath -p=1", "CGO_ENABLED=0", "GOMAXPROCS=2", "GOMEMLIMIT=256MiB")
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

// groupResult is one group's probe report and writer analysis, as cached. A
// probe that failed records its error; its analysis may still be present.
type groupResult struct {
	Probe    probeOutput    `json:"probe"`
	Analysis writerAnalysis `json:"analysis"`
	Error    string         `json:"error,omitempty"`
}

// errIncomplete reports a batch run that left groups uncomputed.
var errIncomplete = errors.New("groups remain")

// probe analyzes one tag's archived source, then compiles and runs the shape
// probe against it. The analysis runs first, so the probe's added files are
// never part of the analyzed binary.
func probe(commit string, entries []string) (out probeOutput, analysis writerAnalysis, err error) {
	analysis = writerAnalysis{Analyzer: analyzerVersion(), Refused: "the source was not analyzed"}
	dir, err := os.MkdirTemp("", "release-restore-probe-")
	if err != nil {
		return out, analysis, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tarball := filepath.Join(dir, "source.tar")
	if _, err := git("archive", "--format=tar", "-o", tarball, commit); err != nil {
		return out, analysis, err
	}
	if _, err := command(dir, "tar", "-xf", tarball); err != nil {
		return out, analysis, err
	}
	analysis = analyzeWriters(dir)
	module, err := command(dir, "go", "list", "-m")
	if err != nil {
		return out, analysis, err
	}
	err = errors.Join(os.Mkdir(filepath.Join(dir, "releaserestoreprobe"), 0o750),
		os.WriteFile(filepath.Join(dir, "internal/store/zz_release_restore_probe.go"), []byte(strings.Replace(probeTemplate, "\t// TYPES\n", strings.Join(entries, ""), 1)), 0o600),
		os.WriteFile(filepath.Join(dir, "releaserestoreprobe/main.go"), []byte(strings.ReplaceAll(probeMain, "MODULE", strings.TrimSpace(string(module)))), 0o600))
	if err != nil {
		return out, analysis, err
	}
	report := filepath.Join(dir, "probe.json")
	if _, err := command(dir, "go", "run", "./releaserestoreprobe", report); err != nil {
		return out, analysis, fmt.Errorf("probe %s: %w", commit, err)
	}
	data, err := os.ReadFile(filepath.Clean(report))
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	return out, analysis, err
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

// inspect parses one tag's registry and names its group: the exact internal
// and cmd trees, go.mod, go.sum, toolchain, and generator source.
func inspect(result row, toolchain string) (row, []string) {
	src, err := command("", "git", "show", result.Commit+":internal/store/operation.go")
	var entries []string
	if err == nil {
		result.Registry = digest(src)
		entries, err = registryEntries(src)
	}
	var inputs string
	if err == nil {
		inputs, err = git("rev-parse", result.Commit+":internal", result.Commit+":cmd", result.Commit+":go.mod", result.Commit+":go.sum")
	}
	if err != nil {
		result.Status, result.Detail = "inventory_refused", err.Error()
		result.Excluded = err.Error() == untypedReason && strings.Contains(excludedFormA, " "+result.Tag+" ")
		return result, nil
	}
	result.Group = digest([]byte(inputs + "\n" + toolchain + "\n" + generatorSource))
	return result, entries
}

// Go memory targets reduce collection pressure; the census runner separately
// enforces a kernel limit on the original job and all its compiler children.
func limitResources() {
	if debug.SetMemoryLimit(-1) > 512<<20 {
		debug.SetMemoryLimit(512 << 20)
	}
	if runtime.GOMAXPROCS(0) > 2 {
		runtime.GOMAXPROCS(2)
	}
}

func main() {
	limitResources()
	output := flag.String("out", "cmd/concord/testdata/release_restore/writers.json", "inventory to write")
	coverageOut := flag.String("coverage", "cmd/concord/testdata/release_restore/coverage.json", "shape coverage to write")
	cache := flag.String("cache", "", "directory that keeps each group's probe result and analysis between runs")
	batch := flag.Int("batch", 0, "with -cache, compute at most this many missing groups, then exit 3 if any remain")
	var witnesses []string
	flag.Func("witnesses", "witness log (JSON lines) to bind shapes to: a historical corpus or release gate log; repeatable", func(path string) error {
		witnesses = append(witnesses, path)
		return nil
	})
	analyze := flag.String("analyze", "", "print the writer analysis of one release source tree and exit, to review its sites")
	flag.Parse()
	if *analyze != "" {
		data, err := json.MarshalIndent(analyzeWriters(*analyze), "", " ")
		if err == nil {
			_, err = os.Stdout.Write(append(data, '\n'))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "release-restore-inventory:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*output, *coverageOut, *cache, *batch, witnesses); err != nil {
		fmt.Fprintln(os.Stderr, "release-restore-inventory:", err)
		if errors.Is(err, errIncomplete) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

// cached reads one group's kept result.
func cached(cache, key string) (groupResult, bool) {
	var result groupResult
	if cache == "" {
		return result, false
	}
	data, err := os.ReadFile(filepath.Clean(cachePath(cache, key)))
	return result, err == nil && json.Unmarshal(data, &result) == nil
}

func cachePath(cache, key string) string {
	return filepath.Join(cache, strings.TrimPrefix(key, "sha256:")+".json")
}

// keep writes one group's result through a rename, so a reader never sees a
// partial file.
func keep(cache, key string, result groupResult) error {
	if cache == "" {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(cache, ".group-")
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err = errors.Join(err, file.Close()); err == nil {
		err = os.Rename(file.Name(), cachePath(cache, key))
	}
	if err != nil {
		_ = os.Remove(file.Name())
	}
	return err
}

func run(output, coverageOut, cache string, batch int, witnessLogs []string) error {
	if len(witnessLogs) == 0 {
		return errors.New("-witnesses is required: shapes without a witness join are not coverage")
	}
	if batch < 0 || batch > 0 && cache == "" {
		return errors.New("-batch needs -cache and a positive count")
	}
	if cache != "" {
		if err := os.MkdirAll(cache, 0o750); err != nil {
			return err
		}
	}
	var witnesses []witness
	for _, log := range witnessLogs {
		rows, err := readWitnesses(log)
		if err != nil {
			return err
		}
		witnesses = append(witnesses, rows...)
	}
	table, err := parseExemptions(exemptionsSource)
	if err != nil {
		return err
	}
	toolchain, err := command("", "go", "version")
	if err != nil {
		return err
	}
	tags, err := listTags()
	if err != nil || len(tags) == 0 {
		return fmt.Errorf("release tags unavailable or empty: %v", err)
	}
	inv := inventory{Schema: schemaID, Generator: "go run ./scripts/release-restore-inventory", Groups: map[string][]registration{}, Analyses: map[string]writerAnalysis{}, Types: map[string]json.RawMessage{}}
	type pending struct {
		commit  string
		entries []string
	}
	work := map[string]pending{}
	for _, ref := range tags {
		result, entries := inspect(ref, strings.TrimSpace(string(toolchain)))
		if result.Group != "" {
			work[result.Group] = pending{result.Commit, entries}
		}
		inv.Rows = append(inv.Rows, result)
	}
	results := map[string]groupResult{}
	var missing []string
	for key := range work {
		if result, ok := cached(cache, key); ok {
			results[key] = result
		} else {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	remaining := 0
	if batch > 0 && len(missing) > batch {
		missing, remaining = missing[:batch], len(missing)-batch
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var keepErr error
	next := make(chan string)
	for range workers {
		wg.Go(func() {
			for key := range next {
				out, analysis, err := probe(work[key].commit, work[key].entries)
				result := groupResult{Probe: out, Analysis: analysis}
				if err != nil {
					result.Error = err.Error()
				}
				err = keep(cache, key, result)
				mu.Lock()
				results[key], keepErr = result, errors.Join(keepErr, err)
				mu.Unlock()
			}
		})
	}
	for _, key := range missing {
		next <- key
	}
	close(next)
	wg.Wait()
	if keepErr != nil {
		return keepErr
	}
	if remaining > 0 {
		return fmt.Errorf("%w: %d of %d groups are not computed yet; rerun with the same -cache", errIncomplete, remaining, len(work))
	}
	failures := map[string]error{}
	for key, result := range results {
		inv.Analyses[key] = result.Analysis
		var err error
		if result.Error != "" {
			err = errors.New(result.Error)
		} else {
			inv.Groups[key], err = group(len(work[key].entries), result.Probe, inv.Types)
		}
		if err != nil {
			delete(inv.Groups, key)
			failures[key] = err
		}
	}
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
	coverage, err := cover(inv, witnesses, table)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(coverage, "", " ")
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
	if len(coverage.Unbound) > 0 {
		held = append(held, errors.New("coverage held: a writer-produced row binds to no registered shape or accepted version of its release; see coverage"))
	}
	for _, older := range coverage.Older {
		if !older.Covered {
			held = append(held, errors.New("coverage held: an older accepted version lacks a same-release witness; see coverage"))
			break
		}
	}
	for _, shape := range coverage.Shapes {
		if !shape.Covered {
			held = append(held, errors.New("coverage held: a written shape has no complete witness; see coverage"))
			break
		}
	}
	return errors.Join(held...)
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
