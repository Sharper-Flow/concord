package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// A writer finding records, for one release, the analyzer that ran, the pairs
// its resolved sites write, and every open site of its analysis. An open site
// that a reviewed exemption covers is reported as exempted; every other open
// site is held. A discharge is the finding's computed proof that one
// registered (kind, version) has no writer at that release: no resolved
// write, held site, replay-held kind, or upcast-held pair can emit it. A
// refused or missing analysis, and a held site that names no pairs, discharge
// nothing.
//
// Four exemption classes are admitted, each by a recorded ruling. The first
// two are keyed by function, exact expression, and function digest:
//
//   - read_back: a site that reads a stored row's kind and version back.
//   - replay_decoder: the v0.3-v0.39 workflow replay decoder, which builds
//     work_item events from caller input. Its finding names, as
//     replay_held_kinds, every registered kind whose fold is not shown to
//     refuse a work_item subject, or "*" when the registry is not a fact or
//     checkSubject differs from the entry's reviewed digest. Every version of
//     a named kind stays held at that release.
//   - upcast_confinement: a site the analysis confines to an upcaster
//     (writerSite.Confinement), keyed by the dispatcher's name and digest,
//     one exact guard it holds, its callers' names and digests, and, when the
//     release has a recorded-schema adapt path, the proven adapt route: the
//     adapt caller's name and digest and its forwarders'. An entry without
//     adapt keys matches only a release whose analysis found no adapt route;
//     an adapt-keyed entry matches only the exact route. The reviewed
//     dispatcher refuses any result whose version is not the current version
//     of the registration it is passed, the reviewed callers pass the
//     registry registration of the event's kind, and a recorded-schema adapt
//     between them may change the kind, so a covered site can still produce
//     the current version of a kind with an older supported version or an
//     upcaster, and of any kind at that version when the kind also has
//     recorded schemas: the finding names those pairs as upcast_held_pairs.
//
//   - comparison_only: an assignment whose function's Event result reaches,
//     through its one static caller, only a bool comparator, keyed by the
//     function's name and digest and the caller's and comparator's digests;
//     the site expression is free, as in the censused table.
//
// Any drift of a keyed digest or expression leaves the site held.

//go:embed exemptions.v1.json
var exemptionsSource []byte

const exemptionsSchema = "concord.release-writer-exemptions.v1"

// exemption is one reviewed entry; Check records how its condition was
// checked.
type exemption struct {
	ID             string `json:"id"`
	Class          string `json:"class"`
	Function       string `json:"function,omitempty"`
	Expression     string `json:"expression,omitempty"`
	FunctionDigest string `json:"function_digest,omitempty"`
	// Dispatcher, DispatcherDigest, and Guard key an upcast_confinement
	// entry; no other class has them.
	Dispatcher       string      `json:"dispatcher,omitempty"`
	DispatcherDigest string      `json:"dispatcher_digest,omitempty"`
	Guard            string      `json:"guard,omitempty"`
	Callers          []namedHash `json:"callers,omitempty"`
	// AdaptCaller, AdaptCallerDigest, Forwarders, and AdaptValues key an
	// upcast_confinement entry's adapt route, all or none together; an entry
	// without them demands a route-free release.
	AdaptCaller       string      `json:"adapt_caller,omitempty"`
	AdaptCallerDigest string      `json:"adapt_caller_digest,omitempty"`
	Forwarders        []namedHash `json:"forwarders,omitempty"`
	AdaptValues       []namedHash `json:"adapt_values,omitempty"`
	// CheckSubjectDigest is the reviewed checkSubject a replay_decoder's
	// fold refusals rely on; no other class has one.
	CheckSubjectDigest string `json:"check_subject_digest,omitempty"`
	// Caller, CallerDigest, Comparator, and ComparatorDigest key a
	// comparison_only entry; no other class has them.
	Caller           string `json:"caller,omitempty"`
	CallerDigest     string `json:"caller_digest,omitempty"`
	Comparator       string `json:"comparator,omitempty"`
	ComparatorDigest string `json:"comparator_digest,omitempty"`
	Check            string `json:"check"`
}

type exemptionTable struct {
	Schema  string      `json:"schema"`
	Entries []exemption `json:"entries"`
	digest  string
}

// parseExemptions refuses the whole table for an unknown field or class, a
// missing key, identity, or check record, or a repeated identity or key.
func parseExemptions(data []byte) (exemptionTable, error) {
	var table exemptionTable
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&table); err != nil {
		return table, fmt.Errorf("exemption table: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return table, fmt.Errorf("exemption table: data after the table")
	}
	if table.Schema != exemptionsSchema {
		return table, fmt.Errorf("exemption table schema %q, want %q", table.Schema, exemptionsSchema)
	}
	seen := map[string]bool{}
	for _, e := range table.Entries {
		site := []string{e.Function, e.Expression, e.FunctionDigest}
		callers := ""
		if len(e.Callers) > 0 {
			data, err := json.Marshal(e.Callers)
			if err != nil {
				return table, err
			}
			callers = string(data)
		}
		route := ""
		if (e.AdaptCaller == "") != (e.AdaptCallerDigest == "") || (e.AdaptCaller == "") != (len(e.Forwarders) == 0) || (e.AdaptCaller == "") != (len(e.AdaptValues) == 0) {
			return table, fmt.Errorf("exemption %q names only part of an adapt route", e.ID)
		}
		if e.AdaptCaller != "" {
			forwarders, err := json.Marshal(e.Forwarders)
			if err != nil {
				return table, err
			}
			values, err := json.Marshal(e.AdaptValues)
			if err != nil {
				return table, err
			}
			route = e.AdaptCaller + "\x00" + e.AdaptCallerDigest + "\x00" + string(forwarders) + "\x00" + string(values)
		}
		upcast := []string{e.Dispatcher, e.DispatcherDigest, e.Guard, callers}
		if route != "" {
			upcast = append(upcast, route)
		}
		check := []string{e.CheckSubjectDigest}
		comparison := []string{e.Caller, e.CallerDigest, e.Comparator, e.ComparatorDigest}
		keys, absent := site, slices.Concat(upcast, check, comparison)
		switch e.Class {
		case "read_back":
		case "replay_decoder":
			keys, absent = slices.Concat(site, check), slices.Concat(upcast, comparison)
		case "upcast_confinement":
			keys, absent = upcast, slices.Concat(site, check, comparison)
		case "comparison_only":
			keys, absent = slices.Concat([]string{e.Function, e.FunctionDigest}, comparison), slices.Concat(upcast, check)
		default:
			return table, fmt.Errorf("exemption %q has unknown class %q", e.ID, e.Class)
		}
		if slices.Contains(append([]string{e.ID, e.Check}, keys...), "") || slices.ContainsFunc(absent, func(key string) bool { return key != "" }) {
			return table, fmt.Errorf("exemption %q lacks a key of its class, its identity, or its check record, or has another class's key", e.ID)
		}
		key := strings.Join(slices.Concat(site, upcast, []string{e.Caller, e.CallerDigest, e.Comparator, e.ComparatorDigest}), "\x00")
		if seen[e.ID] || seen[key] {
			return table, fmt.Errorf("exemption %q is repeated", e.ID)
		}
		seen[e.ID], seen[key] = true, true
	}
	table.digest = digest(data)
	return table, nil
}

// covers reports whether an entry covers one open site of an analysis.
func (e exemption) covers(site writerSite, analysis writerAnalysis) bool {
	if e.Class == "upcast_confinement" {
		d, route := analysis.Dispatcher, analysis.Adapt
		common := site.Confinement == "upcaster" && d != nil && d.Function == e.Dispatcher && d.FunctionDigest == e.DispatcherDigest && slices.Contains(d.Guards, e.Guard) && slices.Equal(d.Callers, e.Callers)
		if e.AdaptCaller == "" {
			return common && route == nil
		}
		return common && route != nil && route.Function == e.AdaptCaller && route.FunctionDigest == e.AdaptCallerDigest && slices.Equal(route.Forwarders, e.Forwarders) && slices.Equal(route.Values, e.AdaptValues)
	}
	if e.Class == "comparison_only" {
		c := site.Comparison
		return c != nil && site.Function == e.Function && site.FunctionDigest == e.FunctionDigest && c.Caller == e.Caller && c.CallerDigest == e.CallerDigest && c.Comparator == e.Comparator && c.ComparatorDigest == e.ComparatorDigest
	}
	return site.Function == e.Function && site.Expression == e.Expression && site.FunctionDigest == e.FunctionDigest
}

// writerFinding is one release's writer result: the analyzer and exemption
// table it used, its refusal, the pairs it writes, the sites left held, the
// exempted sites, the kinds an exempted replay decoder holds, and the pairs
// an exempted upcaster can still produce.
// discharge is one obligation a finding's exemptions proved unwritable at
// that release, with the exemption entries the proof relied on.
type discharge struct {
	Kind           string   `json:"kind"`
	PayloadVersion int      `json:"payload_version"`
	Exemptions     []string `json:"exemptions"`
}

type writerFinding struct {
	Tag           string         `json:"tag"`
	Analyzer      string         `json:"analyzer"`
	Exemptions    string         `json:"exemptions"`
	Refused       string         `json:"refused,omitempty"`
	Writes        []string       `json:"writes,omitempty"`
	Held          []writerSite   `json:"held_sites,omitempty"`
	Exempted      []exemptedSite `json:"exempted_sites,omitempty"`
	ReplayHeld    []string       `json:"replay_held_kinds,omitempty"`
	UpcastHeld    []string       `json:"upcast_held_pairs,omitempty"`
	Discharged    []discharge    `json:"discharged"`
	Undischarged  []string       `json:"undischarged"`
	FindingSource string         `json:"finding_source"`
}

// pairMatches reports whether a site pair can produce (kind, version) at a
// release whose registered current versions are current. A version form the
// matcher does not know matches every version.
func pairMatches(pair, kind string, version int, current map[string]int) bool {
	k, v, ok := strings.Cut(pair, " v")
	if !ok {
		return true
	}
	if k != "*" && k != kind {
		return false
	}
	if v == "*" {
		return true
	}
	if other, isCurrent := strings.CutPrefix(v, "cur:"); isCurrent {
		cur, registered := current[other]
		return !registered || cur == version
	}
	n, err := strconv.Atoi(v)
	return err != nil || n == version
}

// exemptedSite is an open site and the exemption that covers it.
type exemptedSite struct {
	writerSite
	Exemption string `json:"exemption"`
	Class     string `json:"class"`
}

//go:embed findings.go
var findingsSource string

//go:embed coverage.go
var coverageSource string

// findingSource names the code that turns analyses into findings and coverage.
var findingSource = digest([]byte(findingsSource + coverageSource))

// writerFindings derives one finding per inventoried release. A release with
// no analysis, or a refused one, holds no sites and records the refusal.
func writerFindings(inv inventory, table exemptionTable) []writerFinding {
	findings := []writerFinding{}
	for _, result := range inv.Rows {
		if result.Status != "inventoried" {
			continue
		}
		analysis, analyzed := inv.Analyses[result.Group]
		finding := writerFinding{Tag: result.Tag, Analyzer: analysis.Analyzer, Exemptions: table.digest, Discharged: []discharge{}, Undischarged: []string{}, FindingSource: findingSource}
		switch {
		case !analyzed:
			finding.Refused = "no analysis for the release's group"
		case analysis.Refused != "":
			finding.Refused = analysis.Refused
		default:
			finding.Writes = analysis.Writes
			for _, site := range analysis.Open {
				i := slices.IndexFunc(table.Entries, func(e exemption) bool { return e.covers(site, analysis) })
				if i < 0 {
					finding.Held = append(finding.Held, site)
					continue
				}
				e := table.Entries[i]
				finding.Exempted = append(finding.Exempted, exemptedSite{site, e.ID, e.Class})
				if e.Class == "replay_decoder" {
					held := analysis.WorkItemFolds
					if analysis.CheckSubject != e.CheckSubjectDigest {
						held = []string{"*"}
					}
					finding.ReplayHeld = slices.Compact(slices.Sorted(slices.Values(append(finding.ReplayHeld, held...))))
				}
				if e.Class == "upcast_confinement" && finding.UpcastHeld == nil {
					finding.UpcastHeld = []string{}
					for _, r := range inv.Groups[result.Group] {
						if r.MinSupported < r.CurrentVersion || len(r.Upcasters) > 0 {
							finding.UpcastHeld = append(finding.UpcastHeld, r.Kind+" v"+strconv.Itoa(r.CurrentVersion))
						}
					}
					finding.UpcastHeld = slices.Compact(slices.Sorted(slices.Values(finding.UpcastHeld)))
				}
			}
			// A discharge is the exemption table's proof that a registered
			// (kind, version) has no writer at this release: every resolved
			// write and held site must miss the pair, the replay decoder
			// must not hold its kind, and no exempted upcaster can produce
			// it — a pair an upcast_held entry names stays an obligation. A
			// held site that names no pairs misses nothing provably, so it
			// blocks every pair; the live analyzer always names a wildcard
			// pair, so only an analysis it did not produce can get here.
			current := map[string]int{}
			for _, r := range inv.Groups[result.Group] {
				current[r.Kind] = r.CurrentVersion
			}
			replay := map[string]bool{}
			for _, kind := range finding.ReplayHeld {
				replay[kind] = true
			}
			heldBlind := slices.ContainsFunc(finding.Held, func(site writerSite) bool { return len(site.Pairs) == 0 })
			for _, r := range inv.Groups[result.Group] {
				for version := r.MinSupported; version <= r.CurrentVersion; version++ {
					blocked := heldBlind || finding.Refused != "" || replay[r.Kind] || replay["*"]
					for _, held := range finding.UpcastHeld {
						blocked = blocked || pairMatches(held, r.Kind, version, current)
					}
					for _, write := range analysis.Writes {
						blocked = blocked || pairMatches(write, r.Kind, version, current)
					}
					for _, site := range finding.Held {
						for _, pair := range site.Pairs {
							blocked = blocked || pairMatches(pair, r.Kind, version, current)
						}
					}
					pair := r.Kind + " v" + strconv.Itoa(version)
					if blocked {
						finding.Undischarged = append(finding.Undischarged, pair)
						continue
					}
					relied := []string{}
					for _, covered := range finding.Exempted {
						for _, pair := range covered.Pairs {
							if pairMatches(pair, r.Kind, version, current) && !slices.Contains(relied, covered.Exemption) {
								relied = append(relied, covered.Exemption)
							}
						}
					}
					slices.Sort(relied)
					finding.Discharged = append(finding.Discharged, discharge{r.Kind, version, relied})
				}
			}
		}
		findings = append(findings, finding)
	}
	return findings
}
