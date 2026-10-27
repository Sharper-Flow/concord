package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// nativeGoEventGrade consumes one converter event at a time. It never retains
// Output or a second event stream beside the bounded raw test output.
type nativeGoEventGrade struct {
	pkg               string
	state             map[string]int
	required          []string
	expected          []string
	skipped           []string
	nestedSkips       []string
	packageMode       bool
	started, terminal bool
}

func newNativeGoEventGrade(pkg string, names []string, packageMode bool, expected []string) *nativeGoEventGrade {
	g := &nativeGoEventGrade{pkg: pkg, state: map[string]int{}, required: slices.Clone(names), expected: expected, packageMode: packageMode}
	for _, name := range names {
		g.state[name] = 0
		root, _, _ := strings.Cut(name, "/")
		g.state[root] = 0
	}
	return g
}

func (g *nativeGoEventGrade) consume(action, test, pkg string) error {
	if g.terminal || pkg != g.pkg || action == "fail" {
		return nativeUnavailable("test2json contains conflicting or post-terminal events")
	}
	if action == "start" {
		if g.started || test != "" {
			return nativeUnavailable("test2json contains a conflicting package start")
		}
		g.started = true
		return nil
	}
	if action == "output" {
		return nil
	}
	if test == "" {
		if action != "pass" || !g.started && g.packageMode || len(g.required) == 0 {
			return nativeUnavailable("test2json has no complete package pass")
		}
		for _, phase := range g.state {
			if phase != 2 && phase != 3 {
				return nativeUnavailable("package passed before every witness terminated")
			}
		}
		g.terminal = true
		return nil
	}
	root, _, nested := strings.Cut(test, "/")
	rootPhase, knownRoot := g.state[root]
	phase, known := g.state[test]
	switch action {
	case "run":
		if !g.started && g.packageMode || (!nested && !known) || (nested && (!knownRoot || rootPhase != 1)) || (known && phase != 0) || len(g.state) >= 65536 {
			return nativeUnavailable("test2json contains duplicate or undeclared witness runs")
		}
		g.state[test] = 1
	case "pause", "cont":
		if !known || phase != 1 {
			return nativeUnavailable("test2json continues a witness that is not running")
		}
	case "pass", "skip":
		if !known || phase != 1 || nested && rootPhase != 1 {
			return nativeUnavailable("test2json contains a duplicate or unordered witness terminal")
		}
		if action == "skip" {
			if !nested && (!g.packageMode || !slices.Contains(g.expected, test)) {
				return nativeUnavailable("selected witness was skipped or undeclared: " + test)
			}
			if nested {
				for _, required := range g.required {
					if !g.packageMode && (test == required || strings.HasPrefix(test, required+"/") || strings.HasPrefix(required, test+"/")) {
						return nativeUnavailable("required witness subtree was skipped: " + test)
					}
				}
				if len(g.nestedSkips) >= 64 || len(test) > 256 {
					return nativeUnavailable("nested skip accounting exceeds bounded result")
				}
				g.nestedSkips = append(g.nestedSkips, test)
			} else {
				g.skipped = append(g.skipped, test)
			}
			g.state[test] = 3
		} else {
			g.state[test] = 2
		}
	default:
		return nativeUnavailable("test2json contains an unsupported action")
	}
	return nil
}

func (g *nativeGoEventGrade) finish() error {
	if !g.terminal {
		return nativeUnavailable("test2json has no complete package pass")
	}
	for _, name := range g.required {
		if g.state[name] != 2 && !(g.packageMode && g.state[name] == 3) {
			return nativeUnavailable("required witness did not run and pass: " + name)
		}
	}
	slices.Sort(g.skipped)
	slices.Sort(g.nestedSkips)
	return nil
}

func gradeNativeGoEvents(reader io.Reader, g *nativeGoEventGrade) error {
	s := bufio.NewScanner(reader)
	s.Buffer(make([]byte, 4096), 64*1024)
	for s.Scan() {
		var event struct{ Action, Package, Test string }
		if err := json.Unmarshal(s.Bytes(), &event); err != nil {
			return nativeUnavailable("test2json conversion is malformed")
		}
		if err := g.consume(event.Action, event.Test, event.Package); err != nil {
			return err
		}
	}
	if err := s.Err(); err != nil {
		return nativeUnavailable("test2json event exceeds bounded decoder or stream is incomplete")
	}
	return g.finish()
}

func nativeGoGradeBytes(raw []byte, pkg string, selected []string) error {
	return gradeNativeGoEvents(bytes.NewReader(raw), newNativeGoEventGrade(pkg, selected, false, nil))
}
