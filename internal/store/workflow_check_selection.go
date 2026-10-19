package store

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

// workflowCommandSelectsCheck recognizes selection, not proof. The caller owns
// exact tooling declaration, native execution, immutable subject and source
// ownership, including build selection and module boundaries under the actual
// invocation's recorded environment and flags (not ambient settings).
func workflowCommandSelectsCheck(check FootprintCheck, command []string) bool {
	switch check.Kind {
	case "go_package":
		pkg, ok := workflowPackageCheckPackage(check.Value)
		selection, parsed := workflowParseGoTest(command)
		return ok && parsed && selection.run == "" && workflowPatternsSelectPackage(selection.patterns, pkg)
	case "go_test":
		pkg, name, ok := workflowTestCheckParts(check.Value)
		selection, parsed := workflowParseGoTest(command)
		return ok && parsed && workflowRunSelectsName(selection.run, name) && workflowPatternsSelectPackage(selection.patterns, pkg)
	case "validator":
		return validFootprintPath(check.Value) && !strings.HasPrefix(check.Value, "-") &&
			len(command) == 2 && command[0] == "python3" && command[1] == check.Value
	default:
		return false
	}
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

// workflowValidPackagePattern reports whether a command package pattern is
// one of the bounded shapes: ./<package>, ./<prefix>/..., or ./.... An
// absolute, traversing, excluded, or source-file pattern refuses the whole
// invocation rather than being skipped.
func workflowValidPackagePattern(pattern string) bool {
	rest, ok := strings.CutPrefix(pattern, "./")
	if !ok {
		return false
	}
	if rest == "" || rest == "..." {
		return true
	}
	if prefix, found := strings.CutSuffix(rest, "/..."); found {
		rest = prefix
	}
	if strings.HasSuffix(rest, ".go") {
		return false
	}
	return workflowValidPackagePath(rest)
}

// workflowPatternMatches reports whether one validated pattern selects pkg,
// extending only at directory boundaries: a prefix pattern matches its own
// package or a slash-descendant, never a string-prefix lookalike.
func workflowPatternMatches(pattern, pkg string) bool {
	rest := strings.TrimPrefix(pattern, "./")
	if rest == "..." {
		return true
	}
	if prefix, found := strings.CutSuffix(rest, "/..."); found {
		return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
	}
	return rest == pkg
}

// workflowRunSelectsName applies one -run value to a test's top-level name.
// The value must carry no slash component, because a slash selects subtests
// and subtests are not selection of the parent check, and must compile as a
// Go regexp. The match is unanchored, as go test matches it. go test also
// compiles each top-level alternative alone and rewrites spaces and
// unprintable runes first, so a flag group or such a rune refuses.
func workflowRunSelectsName(run, name string) bool {
	if strings.Contains(run, "/") || strings.Contains(run, "(?") ||
		strings.ContainsFunc(run, func(char rune) bool { return char == ' ' || !strconv.IsPrint(char) }) {
		return false
	}
	expression, err := regexp.Compile(run)
	if err != nil {
		return false
	}
	return expression.MatchString(name)
}

// workflowGoTestSelection is the parsed bounded shape of a go test
// invocation: closed-set flags, then one or more package patterns.
type workflowGoTestSelection struct {
	patterns []string
	run      string
}

// workflowParseGoTest parses `go test [flags] <patterns>` and nothing else.
// Flags admit only the closed set -race, -v, -json, -count, -timeout, and
// -run, each in =value or separate value form where it takes a value, with
// positive count and timeout values and any regexp body for -run. Duplicate
// -run or -count flags refuse instead of guessing precedence, a flag after
// the first pattern refuses, and a package-less invocation refuses. Every
// other flag, including -args, --, -list, -skip, and -tags, refuses.
func workflowParseGoTest(command []string) (workflowGoTestSelection, bool) {
	var selection workflowGoTestSelection
	if len(command) < 3 || command[0] != "go" || command[1] != "test" {
		return selection, false
	}
	countSeen, runSeen := false, false
	for index := 2; index < len(command); index++ {
		token := command[index]
		if !strings.HasPrefix(token, "-") {
			selection.patterns = append(selection.patterns, token)
			continue
		}
		if len(selection.patterns) > 0 {
			return selection, false
		}
		name, value, hasValue := strings.Cut(token, "=")
		switch name {
		case "-race", "-v", "-json":
			if hasValue {
				return selection, false
			}
		case "-count":
			if countSeen {
				return selection, false
			}
			text, ok := workflowFlagValue(command, &index, value, hasValue)
			if !ok {
				return selection, false
			}
			count, err := strconv.Atoi(text)
			if err != nil || count <= 0 {
				return selection, false
			}
			countSeen = true
		case "-timeout":
			text, ok := workflowFlagValue(command, &index, value, hasValue)
			if !ok {
				return selection, false
			}
			duration, err := time.ParseDuration(text)
			if err != nil || duration <= 0 {
				return selection, false
			}
		case "-run":
			if runSeen {
				return selection, false
			}
			text, ok := workflowFlagValue(command, &index, value, hasValue)
			if !ok {
				return selection, false
			}
			selection.run = text
			runSeen = true
		default:
			return selection, false
		}
	}
	if len(selection.patterns) == 0 {
		return selection, false
	}
	return selection, true
}

// workflowFlagValue reads a valued flag's operand: the =value form, or the
// next token, which is consumed even when it starts with a dash, as the go
// flag parser consumes it. A valued flag with no following token refuses.
func workflowFlagValue(command []string, index *int, value string, hasValue bool) (string, bool) {
	if hasValue {
		return value, true
	}
	if *index+1 >= len(command) {
		return "", false
	}
	*index++
	return command[*index], true
}

// workflowPatternsSelectPackage validates every pattern first, so one
// malformed pattern refuses the invocation even when another pattern would
// match, then reports whether any pattern selects pkg.
func workflowPatternsSelectPackage(patterns []string, pkg string) bool {
	for _, pattern := range patterns {
		if !workflowValidPackagePattern(pattern) {
			return false
		}
	}
	for _, pattern := range patterns {
		if workflowPatternMatches(pattern, pkg) {
			return true
		}
	}
	return false
}
