package store

import (
	"regexp"
	"sort"
	"strings"
)

// workflowFootprintChecks retains unknown checks for fail-closed consumers.
// Complete-empty is valid; incomplete never means an empty required set.
func workflowFootprintChecks(footprint WorkSizeFootprint) ([]FootprintCheck, bool) {
	seen := make(map[FootprintCheck]struct{}, len(footprint.Laws)+len(footprint.PackageChecks))
	checks := make([]FootprintCheck, 0, len(footprint.Laws)+len(footprint.PackageChecks))
	add := func(check FootprintCheck) {
		if _, duplicate := seen[check]; duplicate {
			return
		}
		seen[check] = struct{}{}
		checks = append(checks, check)
	}
	for _, law := range footprint.Laws {
		for _, check := range law.Checks {
			add(check)
		}
	}
	for _, check := range footprint.PackageChecks {
		add(FootprintCheck{Kind: check.Kind, Value: check.Value})
	}
	sort.Slice(checks, func(left, right int) bool {
		if checks[left].Kind != checks[right].Kind {
			return checks[left].Kind < checks[right].Kind
		}
		return checks[left].Value < checks[right].Value
	})
	return checks, footprint.ChecksComplete
}

// The validator and generated anchor shapes match scripts/evidence_anchors.py.
var (
	workflowValidatorAnchor = regexp.MustCompile(`^scripts/(check|test)-[a-z0-9-]+\.py$`)
	workflowGeneratedAnchor = regexp.MustCompile(`^[A-Za-z0-9./_-]+#[A-Za-z_][A-Za-z0-9_]*$`)
)

// workflowFootprintCheckCommand derives the one ordinary verify argv whose
// receipt can prove a footprint check. Proof matches this argv exactly; no
// broader or differently spelled invocation is classified as covering the
// check. scenario, adapter_test, and unknown kinds have no receipt consumer
// and derive none. The generated entry resolves the anchor itself and refuses
// a traversing or unresolved path.
func workflowFootprintCheckCommand(check FootprintCheck) ([]string, bool) {
	switch check.Kind {
	case "validator":
		if workflowValidatorAnchor.MatchString(check.Value) {
			return []string{"python3", check.Value}, true
		}
		return nil, false
	case "generated":
		if workflowGeneratedAnchor.MatchString(check.Value) {
			return []string{"python3", "scripts/evidence_anchors.py", "--kind", "generated", "--value", check.Value}, true
		}
		return nil, false
	}
	return workflowGoCheckCommand(check)
}

func workflowGoCheckCommand(check FootprintCheck) ([]string, bool) {
	command := []string{"go", "test", "-json", "-count=1"}
	switch check.Kind {
	case "go_test":
		pkg, name, ok := workflowTestCheckParts(check.Value)
		if !ok {
			return nil, false
		}
		return append(command, "-run", "^"+regexp.QuoteMeta(name)+"$", "./"+pkg), true
	case "go_package":
		pkg, ok := workflowPackageCheckPackage(check.Value)
		if ok {
			return append(command, "./"+pkg), true
		}
	}
	return nil, false
}

// Reject directories omitted by Go's unqualified recursive package patterns.
func workflowExcludedPackageSegment(segment string) bool {
	return segment == "vendor" || segment == "testdata" ||
		strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_")
}

// workflowValidPackagePath reports whether pkg is a contained relative
// package path with no empty, dot, dot-dot, or excluded segment.
func workflowValidPackagePath(pkg string) bool {
	if pkg == "" {
		return false
	}
	for _, segment := range strings.Split(pkg, "/") {
		if segment == "" || segment == "." || segment == ".." || workflowExcludedPackageSegment(segment) {
			return false
		}
	}
	return true
}

// workflowPackageCheckPackage reads the one package a go_package anchor
// names. The value must be the anchor form ./<package>: a single concrete
// contained package, never a wildcard and never an excluded path.
func workflowPackageCheckPackage(value string) (string, bool) {
	pkg, found := strings.CutPrefix(value, "./")
	if !found || strings.Contains(pkg, "...") || !workflowValidPackagePath(pkg) {
		return "", false
	}
	return pkg, true
}

// workflowTestCheckParts splits a go_test anchor into its package and the
// test's top-level name. The name must be an ASCII Go identifier; whether
// the test is registered and build-selected is a caller fact.
func workflowTestCheckParts(value string) (pkg, name string, ok bool) {
	index := strings.LastIndex(value, ".")
	if index <= 0 {
		return "", "", false
	}
	pkg, name = value[:index], value[index+1:]
	if !workflowValidPackagePath(pkg) || !strings.HasPrefix(name, "Test") || len(name) > 4 && name[4] >= 'a' && name[4] <= 'z' {
		return "", "", false
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char == '_':
		case char >= '0' && char <= '9':
		default:
			return "", "", false
		}
	}
	return pkg, name, true
}
