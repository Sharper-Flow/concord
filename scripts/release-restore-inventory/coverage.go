package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

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
	Discharged     []string `json:"discharged"`
	Witnesses      []string `json:"witnesses"`
	Missing        []string `json:"missing_paths,omitempty"`
	Covered        bool     `json:"covered"`
}

// olderCoverage is one accepted payload version below a registration's current
// one. It has no static type: the release's upcaster reads raw JSON. Every
// release that accepts it owes a witness from its own writer at that
// (kind, version); a corpus row names no release and cannot stand in. A
// release whose finding discharges the obligation owes no witness: it is
// reported as discharged, and missing_writers names only a release with
// neither.
type olderCoverage struct {
	Kind           string   `json:"kind"`
	PayloadVersion int      `json:"payload_version"`
	Writers        []string `json:"writers"`
	Discharged     []string `json:"discharged"`
	Witnesses      []string `json:"witnesses"`
	Missing        []string `json:"missing_writers,omitempty"`
	Covered        bool     `json:"covered"`
}

// coverageReport is coverage.json.
type coverageReport struct {
	Schema   string          `json:"schema"`
	Shapes   []shapeCoverage `json:"shapes"`
	Older    []olderCoverage `json:"older_versions"`
	Unbound  []string        `json:"unbound_writer_witnesses"`
	Findings []writerFinding `json:"writer_findings"`
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
// release registers for its (kind, version), and only when it conforms to it,
// or that release's older accepted version when its payload is a JSON object;
// a writer row from an approved excluded release is excluded with it; any
// other writer row is returned as unbound. A release's finding for a
// (kind, version) discharges that release's obligation for it: an older
// version is covered when every release that accepts it is witnessed or
// discharged, and a shape is covered by a conforming witness or when every
// writer that registers it is discharged.
func cover(inv inventory, rows []witness, table exemptionTable) (coverageReport, error) {
	findings := writerFindings(inv, table)
	discharged := map[string]bool{}
	for _, finding := range findings {
		for _, d := range finding.Discharged {
			discharged[fmt.Sprintf("%s\x00%s\x00%d", finding.Tag, d.Kind, d.PayloadVersion)] = true
		}
	}
	shapes, older := map[string]*shapeCoverage{}, map[string]*olderCoverage{}
	registered, accepted, excluded := map[string]string{}, map[string]bool{}, map[string]bool{}
	for _, result := range inv.Rows {
		excluded[result.Tag] = result.Excluded
		if result.Status != "inventoried" {
			continue
		}
		for _, r := range inv.Groups[result.Group] {
			registered[fmt.Sprintf("%s\x00%s\x00%d", result.Tag, r.Kind, r.CurrentVersion)] = r.Type
			key := fmt.Sprintf("%s\x00%d\x00%s", r.Kind, r.CurrentVersion, r.Type)
			if shapes[key] == nil {
				shapes[key] = &shapeCoverage{Kind: r.Kind, PayloadVersion: r.CurrentVersion, Type: r.Type, Witnesses: []string{}, Discharged: []string{}}
			}
			shapes[key].Writers = append(shapes[key].Writers, result.Tag)
			if discharged[fmt.Sprintf("%s\x00%s\x00%d", result.Tag, r.Kind, r.CurrentVersion)] {
				shapes[key].Discharged = append(shapes[key].Discharged, result.Tag)
			}
			for version := r.MinSupported; version < r.CurrentVersion; version++ {
				accepted[fmt.Sprintf("%s\x00%s\x00%d", result.Tag, r.Kind, version)] = true
				key := fmt.Sprintf("%s\x00%d", r.Kind, version)
				if older[key] == nil {
					older[key] = &olderCoverage{Kind: r.Kind, PayloadVersion: version, Witnesses: []string{}, Discharged: []string{}}
				}
				older[key].Writers = append(older[key].Writers, result.Tag)
			}
		}
	}
	unbound := []string{}
	bound := map[int]bool{}
	coverage := make([]shapeCoverage, 0, len(shapes))
	for _, s := range shapes {
		var shape any
		if err := json.Unmarshal(inv.Types[s.Type], &shape); err != nil {
			return coverageReport{}, fmt.Errorf("type %s: %w", s.Type, err)
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
		s.Covered = len(tests) > 0 && len(missing) == 0 || len(s.Discharged) == len(s.Writers)
		coverage = append(coverage, *s)
	}
	witnessed := map[string]bool{}
	for i, w := range rows {
		var value map[string]any
		if w.Writer == nil {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", w.Writer.Tag, w.Kind, w.PayloadVersion)
		if !accepted[key] || json.Unmarshal([]byte(w.Payload), &value) != nil || value == nil {
			continue
		}
		bound[i] = true
		witnessed[key] = true
	}
	olderCoverages := make([]olderCoverage, 0, len(older))
	for _, o := range older {
		for _, tag := range o.Writers {
			key := fmt.Sprintf("%s\x00%s\x00%d", tag, o.Kind, o.PayloadVersion)
			switch {
			case witnessed[key]:
				o.Witnesses = append(o.Witnesses, tag)
			case discharged[key]:
				o.Discharged = append(o.Discharged, tag)
			default:
				o.Missing = append(o.Missing, tag)
			}
		}
		o.Covered = len(o.Missing) == 0
		olderCoverages = append(olderCoverages, *o)
	}
	sort.Slice(olderCoverages, func(i, j int) bool {
		a, b := olderCoverages[i], olderCoverages[j]
		return a.Kind < b.Kind || a.Kind == b.Kind && a.PayloadVersion < b.PayloadVersion
	})
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
	return coverageReport{Schema: "concord.release-restore-coverage.v1", Shapes: coverage, Older: olderCoverages, Unbound: unbound, Findings: findings}, nil
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
