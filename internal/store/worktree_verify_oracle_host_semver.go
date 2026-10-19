package store

import (
	"strconv"
	"strings"
)

// nativeSemver is one SemVer 2.0 version. Build metadata never affects
// precedence and is dropped.
type nativeSemver struct {
	major, minor, patch uint64
	pre                 []string
}

// parseNativeSemver parses one strict SemVer 2.0 version.
func parseNativeSemver(s string) (nativeSemver, bool) {
	var v nativeSemver
	core, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !nativeSemverIdentifiers(build, false) {
		return v, false
	}
	core, pre, hasPre := strings.Cut(core, "-")
	if hasPre {
		if !nativeSemverIdentifiers(pre, true) {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := [3]*uint64{&v.major, &v.minor, &v.patch}
	for i, part := range parts {
		n, ok := nativeSemverNumber(part)
		if !ok {
			return v, false
		}
		*nums[i] = n
	}
	return v, true
}

// nativeSemverNumber parses one numeric identifier without a leading zero.
func nativeSemverNumber(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// nativeSemverIdentifiers validates dot-separated prerelease or build
// identifiers. Numeric prerelease identifiers carry no leading zero.
func nativeSemverIdentifiers(s string, prerelease bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		numeric := true
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				numeric = false
			default:
				return false
			}
		}
		if prerelease && numeric && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func compareNativeSemver(a, b nativeSemver) int {
	for _, pair := range [3][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareNativeSemverIdentifier(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// compareNativeSemverIdentifier orders prerelease identifiers: numeric
// identifiers compare numerically and precede alphanumeric ones.
func compareNativeSemverIdentifier(a, b string) int {
	an, aNumeric := nativeSemverNumber(a)
	bn, bNumeric := nativeSemverNumber(b)
	switch {
	case aNumeric && bNumeric:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	}
	return strings.Compare(a, b)
}

// nativeSemverComparator is one desugared primitive comparator. An empty op
// with any set matches every release version.
type nativeSemverComparator struct {
	op  string
	v   nativeSemver
	any bool
}

func (c nativeSemverComparator) test(v nativeSemver) bool {
	if c.any {
		return true
	}
	cmp := compareNativeSemver(v, c.v)
	switch c.op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return cmp == 0
}

// nativeSemverRange is a union of comparator sets: node-semver's range
// model after desugaring caret, tilde, x-range, and hyphen forms.
type nativeSemverRange [][]nativeSemverComparator

// satisfies applies node-semver's default prerelease rule: a prerelease
// version satisfies a set only when some comparator in that set carries a
// prerelease on the same major.minor.patch tuple.
func (r nativeSemverRange) satisfies(v nativeSemver) bool {
	for _, set := range r {
		ok := true
		for _, c := range set {
			if !c.test(v) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if len(v.pre) == 0 {
			return true
		}
		for _, c := range set {
			if !c.any && len(c.v.pre) > 0 && c.v.major == v.major && c.v.minor == v.minor && c.v.patch == v.patch {
				return true
			}
		}
	}
	return false
}

// nativeSemverPartial is one possibly partial version operand: missing or
// wildcard components are x-ranges.
type nativeSemverPartial struct {
	nums [3]uint64
	have int // count of concrete leading numeric components
	pre  []string
}

func parseNativeSemverPartial(s string) (nativeSemverPartial, bool) {
	var p nativeSemverPartial
	s = strings.TrimPrefix(strings.TrimPrefix(s, "="), "v")
	core, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !nativeSemverIdentifiers(build, false) {
		return p, false
	}
	core, pre, hasPre := strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return p, false
	}
	wild := false
	for i, part := range parts {
		if part == "x" || part == "X" || part == "*" {
			wild = true
			continue
		}
		if wild {
			return p, false
		}
		n, ok := nativeSemverNumber(part)
		if !ok {
			return p, false
		}
		p.nums[i] = n
		p.have = i + 1
	}
	if hasPre {
		if p.have != 3 || !nativeSemverIdentifiers(pre, true) {
			return p, false
		}
		p.pre = strings.Split(pre, ".")
	}
	return p, true
}

func (p nativeSemverPartial) version() nativeSemver {
	return nativeSemver{major: p.nums[0], minor: p.nums[1], patch: p.nums[2], pre: p.pre}
}

// nativeSemverZeroPre is the "-0" prerelease node-semver appends to an
// exclusive upper bound so no prerelease of that bound slips under it.
var nativeSemverZeroPre = []string{"0"}

func nativeSemverBound(major, minor, patch uint64) nativeSemver {
	return nativeSemver{major: major, minor: minor, patch: patch, pre: nativeSemverZeroPre}
}

// xRange desugars a partial operand into its inclusive-exclusive bounds.
func (p nativeSemverPartial) xRange() []nativeSemverComparator {
	switch p.have {
	case 0:
		return []nativeSemverComparator{{any: true}}
	case 1:
		return []nativeSemverComparator{{op: ">=", v: nativeSemver{major: p.nums[0]}}, {op: "<", v: nativeSemverBound(p.nums[0]+1, 0, 0)}}
	case 2:
		return []nativeSemverComparator{{op: ">=", v: nativeSemver{major: p.nums[0], minor: p.nums[1]}}, {op: "<", v: nativeSemverBound(p.nums[0], p.nums[1]+1, 0)}}
	}
	return []nativeSemverComparator{{op: "", v: p.version()}}
}

// parseNativeSemverRange parses the closed node-semver range grammar:
// primitive comparators, bare and partial versions, caret, tilde, x-ranges,
// hyphen ranges, whitespace AND, and "||" unions. Dist-tags, URLs, and
// protocol specifiers are not ranges and refuse.
func parseNativeSemverRange(s string) (nativeSemverRange, bool) {
	var r nativeSemverRange
	for _, alt := range strings.Split(s, "||") {
		set, ok := parseNativeSemverSet(strings.TrimSpace(alt))
		if !ok {
			return nil, false
		}
		r = append(r, set)
	}
	return r, true
}

func parseNativeSemverSet(s string) ([]nativeSemverComparator, bool) {
	if s == "" {
		return []nativeSemverComparator{{any: true}}, true
	}
	tokens := nativeSemverTokens(s)
	if len(tokens) == 3 && tokens[1] == "-" {
		lo, okLo := parseNativeSemverPartial(tokens[0])
		hi, okHi := parseNativeSemverPartial(tokens[2])
		if !okLo || !okHi {
			return nil, false
		}
		var set []nativeSemverComparator
		if lo.have > 0 {
			set = append(set, nativeSemverComparator{op: ">=", v: lo.version()})
		}
		switch hi.have {
		case 0:
		case 1:
			set = append(set, nativeSemverComparator{op: "<", v: nativeSemverBound(hi.nums[0]+1, 0, 0)})
		case 2:
			set = append(set, nativeSemverComparator{op: "<", v: nativeSemverBound(hi.nums[0], hi.nums[1]+1, 0)})
		default:
			set = append(set, nativeSemverComparator{op: "<=", v: hi.version()})
		}
		if len(set) == 0 {
			set = append(set, nativeSemverComparator{any: true})
		}
		return set, true
	}
	var set []nativeSemverComparator
	for _, token := range tokens {
		if token == "-" {
			return nil, false
		}
		comparators, ok := parseNativeSemverToken(token)
		if !ok {
			return nil, false
		}
		set = append(set, comparators...)
	}
	return set, true
}

// nativeSemverTokens splits one comparator set on whitespace and joins an
// operator separated from its operand by spaces (">= 1.2.3").
func nativeSemverTokens(s string) []string {
	var tokens []string
	for _, field := range strings.Fields(s) {
		if n := len(tokens); n > 0 && strings.Trim(tokens[n-1], "<>=~^") == "" && tokens[n-1] != "" {
			tokens[n-1] += field
			continue
		}
		tokens = append(tokens, field)
	}
	return tokens
}

func parseNativeSemverToken(token string) ([]nativeSemverComparator, bool) {
	switch {
	case strings.HasPrefix(token, "^"):
		p, ok := parseNativeSemverPartial(token[1:])
		if !ok {
			return nil, false
		}
		return p.caret(), true
	case strings.HasPrefix(token, "~>"), strings.HasPrefix(token, "~"):
		p, ok := parseNativeSemverPartial(strings.TrimPrefix(strings.TrimPrefix(token, "~"), ">"))
		if !ok {
			return nil, false
		}
		return p.tilde(), true
	}
	op := ""
	for _, candidate := range []string{"<=", ">=", "<", ">", "="} {
		if strings.HasPrefix(token, candidate) {
			op = candidate
			break
		}
	}
	p, ok := parseNativeSemverPartial(strings.TrimPrefix(token, op))
	if !ok {
		return nil, false
	}
	switch {
	case op == "" || op == "=":
		return p.xRange(), true
	case p.have == 3:
		return []nativeSemverComparator{{op: op, v: p.version()}}, true
	}
	// Partial operands under a primitive operator desugar to the bound the
	// x-range implies.
	if p.have == 0 {
		if op == "<" || op == ">" {
			return []nativeSemverComparator{{op: "<", v: nativeSemverBound(0, 0, 0)}}, true
		}
		return []nativeSemverComparator{{any: true}}, true
	}
	lo := nativeSemver{major: p.nums[0], minor: p.nums[1]}
	var next nativeSemver
	if p.have == 1 {
		next = nativeSemver{major: p.nums[0] + 1}
	} else {
		next = nativeSemver{major: p.nums[0], minor: p.nums[1] + 1}
	}
	switch op {
	case ">":
		return []nativeSemverComparator{{op: ">=", v: next}}, true
	case ">=":
		return []nativeSemverComparator{{op: ">=", v: lo}}, true
	case "<":
		return []nativeSemverComparator{{op: "<", v: nativeSemver{major: lo.major, minor: lo.minor, pre: nativeSemverZeroPre}}}, true
	}
	return []nativeSemverComparator{{op: "<", v: nativeSemver{major: next.major, minor: next.minor, pre: nativeSemverZeroPre}}}, true
}

// tilde allows patch-level changes when a minor is given, minor-level
// changes otherwise.
func (p nativeSemverPartial) tilde() []nativeSemverComparator {
	switch p.have {
	case 0, 1:
		return p.xRange()
	}
	return []nativeSemverComparator{{op: ">=", v: p.version()}, {op: "<", v: nativeSemverBound(p.nums[0], p.nums[1]+1, 0)}}
}

// caret allows changes that do not modify the left-most nonzero component.
func (p nativeSemverPartial) caret() []nativeSemverComparator {
	major, minor, patch := p.nums[0], p.nums[1], p.nums[2]
	switch {
	case p.have == 0:
		return p.xRange()
	case p.have == 1:
		return []nativeSemverComparator{{op: ">=", v: nativeSemver{major: major}}, {op: "<", v: nativeSemverBound(major+1, 0, 0)}}
	case p.have == 2:
		if major == 0 {
			return []nativeSemverComparator{{op: ">=", v: nativeSemver{minor: minor}}, {op: "<", v: nativeSemverBound(0, minor+1, 0)}}
		}
		return []nativeSemverComparator{{op: ">=", v: nativeSemver{major: major, minor: minor}}, {op: "<", v: nativeSemverBound(major+1, 0, 0)}}
	}
	lo := nativeSemverComparator{op: ">=", v: p.version()}
	switch {
	case major != 0:
		return []nativeSemverComparator{lo, {op: "<", v: nativeSemverBound(major+1, 0, 0)}}
	case minor != 0:
		return []nativeSemverComparator{lo, {op: "<", v: nativeSemverBound(0, minor+1, 0)}}
	}
	return []nativeSemverComparator{lo, {op: "<", v: nativeSemverBound(0, 0, patch+1)}}
}
