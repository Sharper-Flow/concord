package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// A writer finding records, for one release, the analyzer that ran, the pairs
// its resolved sites write, and every open site of its analysis. An open site
// that a reviewed exemption covers is reported as exempted; every other open
// site is held. Neither a reported write nor an exemption is a discharge: no
// finding discharges a release's obligation for any (kind, version).
//
// Two exemption classes are admitted, each by a recorded ruling, and each
// keyed by function, exact expression, and function digest:
//
//   - read_back: a site that reads a stored row's kind and version back.
//   - replay_decoder: the v0.3-v0.39 workflow replay decoder, which builds
//     work_item events from caller input. It holds every (kind, version) of
//     its release.
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
	Function       string `json:"function"`
	Expression     string `json:"expression"`
	FunctionDigest string `json:"function_digest"`
	Check          string `json:"check"`
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
		if e.Class != "read_back" && e.Class != "replay_decoder" {
			return table, fmt.Errorf("exemption %q has unknown class %q", e.ID, e.Class)
		}
		if slices.Contains([]string{e.ID, e.Function, e.Expression, e.FunctionDigest, e.Check}, "") {
			return table, fmt.Errorf("exemption %q lacks a key, its identity, or its check record", e.ID)
		}
		key := e.Function + "\x00" + e.Expression + "\x00" + e.FunctionDigest
		if seen[e.ID] || seen[key] {
			return table, fmt.Errorf("exemption %q is repeated", e.ID)
		}
		seen[e.ID], seen[key] = true, true
	}
	table.digest = digest(data)
	return table, nil
}

// covers reports whether an entry covers one open site.
func (e exemption) covers(site writerSite) bool {
	return site.Function == e.Function && site.Expression == e.Expression && site.FunctionDigest == e.FunctionDigest
}

// writerFinding is one release's writer result: the analyzer and exemption
// table it used, its refusal, the pairs it writes, the sites left held, and
// the exempted sites.
type writerFinding struct {
	Tag           string         `json:"tag"`
	Analyzer      string         `json:"analyzer"`
	Exemptions    string         `json:"exemptions"`
	Refused       string         `json:"refused,omitempty"`
	Writes        []string       `json:"writes,omitempty"`
	Held          []writerSite   `json:"held_sites,omitempty"`
	Exempted      []exemptedSite `json:"exempted_sites,omitempty"`
	FindingSource string         `json:"finding_source"`
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
		finding := writerFinding{Tag: result.Tag, Analyzer: analysis.Analyzer, Exemptions: table.digest, FindingSource: findingSource}
		switch {
		case !analyzed:
			finding.Refused = "no analysis for the release's group"
		case analysis.Refused != "":
			finding.Refused = analysis.Refused
		default:
			finding.Writes = analysis.Writes
			for _, site := range analysis.Open {
				i := slices.IndexFunc(table.Entries, func(e exemption) bool { return e.covers(site) })
				if i < 0 {
					finding.Held = append(finding.Held, site)
				} else {
					finding.Exempted = append(finding.Exempted, exemptedSite{site, table.Entries[i].ID, table.Entries[i].Class})
				}
			}
		}
		findings = append(findings, finding)
	}
	return findings
}
