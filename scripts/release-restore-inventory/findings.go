package main

import (
	"bytes"
	"embed"
)

// A writer finding records, for one release, the analyzer that ran and every
// open site of its analysis. Every open site is held: no finding discharges a
// release's obligation for any (kind, version).

// writerFinding is one release's writer result: the analyzer it used, its
// refusal, and the sites left held.
type writerFinding struct {
	Tag           string       `json:"tag"`
	Analyzer      string       `json:"analyzer"`
	Refused       string       `json:"refused,omitempty"`
	Held          []writerSite `json:"held_sites,omitempty"`
	FindingSource string       `json:"finding_source"`
}

//go:embed findings.go coverage.go
var findingFiles embed.FS

// findingSource names the code that turns analyses into findings and coverage.
var findingSource = func() string {
	var source bytes.Buffer
	for _, name := range []string{"findings.go", "coverage.go"} {
		data, err := findingFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		source.Write(data)
	}
	return digest(source.Bytes())
}()

// writerFindings derives one finding per inventoried release. A release with
// no analysis, or a refused one, holds no sites and records the refusal.
func writerFindings(inv inventory) []writerFinding {
	findings := []writerFinding{}
	for _, result := range inv.Rows {
		if result.Status != "inventoried" {
			continue
		}
		analysis, analyzed := inv.Analyses[result.Group]
		finding := writerFinding{Tag: result.Tag, Analyzer: analysis.Analyzer, FindingSource: findingSource}
		switch {
		case !analyzed:
			finding.Refused = "no analysis for the release's group"
		case analysis.Refused != "":
			finding.Refused = analysis.Refused
		default:
			finding.Held = analysis.Open
		}
		findings = append(findings, finding)
	}
	return findings
}
