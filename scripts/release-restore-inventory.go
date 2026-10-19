// Command release-restore-inventory records each release tag's event registry
// and payload shapes: it parses the registry literal, then compiles a reflection
// probe against that tag's own source. Tags with identical internal trees,
// go.mod, and go.sum share one probe, which -previous reuses. An unrecognized
// registry entry refuses the tag. Run from the repository root.
package main

import (
	"bytes"
	"crypto/sha256"
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
	"strconv"
	"strings"
	"sync"
)

const (
	schemaID = "concord.release-restore-writers.v1"
	workers  = 4
	// A recorded ruling accepts form A (v0.1.0..v0.37.1) as a named exclusion.
	untypedReason = "registry entries name no payload type (form A); accepted named exclusion from shape coverage"
)

var strictTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type entry struct {
	Kind        string `json:"kind"`
	PayloadType string `json:"payload_type"`
}

type row struct {
	Tag       string `json:"tag"`
	TagObject string `json:"tag_object"`
	Commit    string `json:"commit"`
	Group     string `json:"group,omitempty"`
	Status    string `json:"status"`
	Excluded  bool   `json:"excluded,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type registration struct {
	entry
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
	cmd := exec.Command(name, args...) //nolint:gosec // G204: name is a fixed program, argv values stay separate, and no shell runs.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0")
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

// registryEntries parses one registry source; each entry must name its payload
// type (see typedPayload), and form A composite literals refuse.
func registryEntries(src []byte) ([]entry, error) {
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
	entries := make([]entry, 0, len(literal.Elts))
	for _, element := range literal.Elts {
		pair, _ := element.(*ast.KeyValueExpr)
		var key *ast.BasicLit
		if pair != nil {
			key, _ = pair.Key.(*ast.BasicLit)
		}
		if key == nil {
			return nil, errors.New("registry entry without a literal kind key")
		}
		kind, err := strconv.Unquote(key.Value)
		if _, untyped := pair.Value.(*ast.CompositeLit); untyped {
			return nil, errors.New(untypedReason)
		}
		payload := typedPayload(pair.Value)
		if err != nil || payload == "" {
			return nil, fmt.Errorf("registry entry %q has an unrecognized form", kind)
		}
		entries = append(entries, entry{Kind: kind, PayloadType: payload})
	}
	return entries, nil
}

// typedPayload names T in registerEventKind[T](...) or
// registerScopedEventKind[T](...), optionally inside withRecordedSchemas.
func typedPayload(expr ast.Expr) string {
	call, _ := expr.(*ast.CallExpr)
	if call == nil {
		return ""
	}
	if name(call.Fun) == "withRecordedSchemas" && len(call.Args) == 2 {
		return typedPayload(call.Args[0])
	}
	index, _ := call.Fun.(*ast.IndexExpr)
	if index == nil || (name(index.X) != "registerEventKind" && name(index.X) != "registerScopedEventKind") {
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
func probe(commit string, entries []entry) (probeOutput, error) {
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
	var types strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&types, "\t%q: reflect.TypeOf((*%s)(nil)).Elem(),\n", e.Kind, e.PayloadType)
	}
	err = errors.Join(os.Mkdir(filepath.Join(dir, "releaserestoreprobe"), 0o750),
		os.WriteFile(filepath.Join(dir, "internal/store/zz_release_restore_probe.go"), []byte(strings.Replace(probeTemplate, "\t// TYPES\n", types.String(), 1)), 0o600),
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

// group joins a probe to its parsed entries. The probe's registry must hold
// exactly the parsed kinds, so a kind the parser lost or invented refuses.
func group(entries []entry, out probeOutput, types map[string]json.RawMessage) ([]registration, error) {
	if len(out.Registry) != len(entries) {
		return nil, fmt.Errorf("probe registry has %d kinds, parsed %d", len(out.Registry), len(entries))
	}
	registrations := make([]registration, 0, len(entries))
	for _, e := range entries {
		meta, shape := out.Registry[e.Kind], out.Types[e.Kind]
		if meta == nil || shape == nil {
			return nil, fmt.Errorf("probe lacks kind %q", e.Kind)
		}
		meta.entry, meta.Type = e, digest(shape)
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
func inspect(result row) (row, []entry) {
	src, err := git("show", result.Commit+":internal/store/operation.go")
	var entries []entry
	if err == nil {
		entries, err = registryEntries([]byte(src))
	}
	var inputs string
	if err == nil {
		inputs, err = git("rev-parse", result.Commit+":internal", result.Commit+":go.mod", result.Commit+":go.sum")
	}
	if err != nil {
		result.Status, result.Detail, result.Excluded = "inventory_refused", err.Error(), err.Error() == untypedReason
		return result, nil
	}
	result.Group = digest([]byte(inputs + "\n" + probeTemplate + probeMain))
	return result, entries
}

func main() {
	output := flag.String("out", "cmd/concord/testdata/release_restore/writers.json", "inventory to write")
	previous := flag.String("previous", "", "earlier inventory whose groups are reused by digest")
	flag.Parse()
	if err := run(*output, *previous); err != nil {
		fmt.Fprintln(os.Stderr, "release-restore-inventory:", err)
		os.Exit(1)
	}
}

func run(output, previous string) error {
	tags, err := listTags()
	if err != nil {
		return err
	}
	inv := inventory{Schema: schemaID, Generator: "go run scripts/release-restore-inventory.go", Groups: map[string][]registration{}, Types: map[string]json.RawMessage{}}
	var earlier inventory
	if previous != "" {
		data, err := os.ReadFile(filepath.Clean(previous))
		if err == nil {
			err = json.Unmarshal(data, &earlier)
		}
		if err != nil {
			return fmt.Errorf("previous inventory: %w", err)
		}
	}
	type pending struct {
		commit  string
		entries []entry
	}
	work := map[string]pending{}
	for _, ref := range tags {
		result, entries := inspect(ref)
		if reused, known := earlier.Groups[result.Group]; known {
			inv.Groups[result.Group] = reused
			for _, r := range reused {
				inv.Types[r.Type] = earlier.Types[r.Type]
			}
		} else if result.Group != "" {
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
					inv.Groups[key], err = group(work[key].entries, out, inv.Types)
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
	return os.WriteFile(output, append(data, '\n'), 0o600)
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
		meta := map[string]any{"current_version": registration.CurrentVersion, "min_supported": registration.MinSupported}
		for name, field := range map[string]string{"upcasters": "Upcasters", "recorded_schemas": "RecordedSchemas"} {
			keys := []int{}
			if value := reflect.ValueOf(registration).FieldByName(field); value.IsValid() {
				for _, key := range value.MapKeys() {
					keys = append(keys, int(key.Int()))
				}
			}
			sort.Ints(keys)
			meta[name] = keys
		}
		registry[kind], types[kind] = meta, releaseRestoreShape(payload, map[reflect.Type]bool{})
	}
	return json.Marshal(map[string]any{"registry": registry, "types": types})
}

func releaseRestoreShape(t reflect.Type, seen map[reflect.Type]bool) map[string]any {
	custom := t.Kind() == reflect.Interface
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
		return map[string]any{"kind": "array", "elem": releaseRestoreShape(t.Elem(), seen)}
	case reflect.Map:
		return map[string]any{"kind": "map", "key": t.Key().Kind().String(), "elem": releaseRestoreShape(t.Elem(), seen)}
	case reflect.Struct:
		if seen[t] {
			return map[string]any{"kind": "recursive", "type": t.String()}
		}
		seen[t] = true
		defer delete(seen, t)
		fields, names := []map[string]any{}, map[string]bool{}
		if !releaseRestoreFields(t, seen, &fields, names) {
			return map[string]any{"kind": "custom", "type": t.String(), "reason": "embedded field name conflict"}
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
			if seen[embedded] || !releaseRestoreFields(embedded, seen, fields, names) {
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
