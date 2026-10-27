package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// These fixtures exercise the receipt join only. They do not prepare a
// harness, execute a producer, or stand in for producer qualification.
func footprintInventoryReceipt(kind string, count int) (ProjectResolvedCoverage, NativeOracleResult, WorkerReviewCandidate) {
	var files []NativeOracleFile
	for i := range count {
		files = append(files, NativeOracleFile{Path: fmt.Sprintf("pkg/input%04d_test.go", i), BlobOID: fmt.Sprintf("%040x", i+1), SHA256: nativeDigest([]byte(fmt.Sprintf("input %d", i)))})
	}
	binding := ProjectResolvedCoverage{
		Anchor:       FootprintCheck{Kind: "go_test", Value: "pkg:TestCorpus"},
		RecipeSource: WorkContextReadingSource{Kind: "repository_file", ProjectID: "project", Path: ".concord/oracles/corpus.json", CommitOID: strings.Repeat("a", 40)},
		RecipeDigest: nativeDigest([]byte("recipe")),
		CaseID:       "case:corpus",
		Recipe:       NativeRecipeCase{Kind: kind, Cwd: "pkg", Witnesses: []string{"TestCorpus"}},
		Files:        slices.Clone(files),
	}
	candidate := WorkerReviewCandidate{ProjectID: "project", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40)}
	o := NativeOracleResult{NativeOraclePreparation: NativeOraclePreparation{WorkID: "work", ProjectID: candidate.ProjectID, SubjectCommit: candidate.Commit, ContractVersion: 1, RecipeSource: binding.RecipeSource, ManifestDigest: binding.RecipeDigest, Files: files}}
	if kind == "go_top_level_v1" {
		o.CaseToTestMap = map[string][]string{binding.CaseID: {"TestCorpus"}}
		o.ObservedTestNames = []string{"TestCorpus"}
	} else {
		o.RecipeKind = kind
		o.CaseToWitnessMap = map[string][]string{binding.CaseID: {"TestCorpus"}}
		o.ObservedWitnessIDs = []string{"TestCorpus"}
	}
	// Match the producer's existing sorted-file JSON inventory, independently
	// of the coverage join. Coverage itself may list files in any recipe order.
	if count > 32 {
		o.HarnessInputCount = count
		o.HarnessInputDigest = nativeDigest([]byte(workflowJSON(files)))
		o.Files = nil
	}
	slices.Reverse(binding.Files)
	return binding, o, candidate
}

func TestWorkflowFootprintHarnessInventoryJoins(t *testing.T) {
	for _, kind := range []string{"go_top_level_v1", nativeTargetKind} {
		for _, count := range []int{1, 32, 33, 138, 153} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				binding, o, candidate := footprintInventoryReceipt(kind, count)
				before := slices.Clone(binding.Files)
				if why := nativeOracleReceiptAnchorDeficiency("work", &o, &candidate, 1, &binding); why != "" {
					t.Fatalf("matching pinned inventory refused: %s", why)
				}
				if !slices.Equal(before, binding.Files) {
					t.Fatal("receipt join mutated the shared coverage inputs")
				}
			})
		}
	}
	t.Run("candidate package has no pinned replacement files", func(t *testing.T) {
		binding, o, candidate := footprintInventoryReceipt(nativeGoPackageKind, 0)
		if why := nativeOracleReceiptAnchorDeficiency("work", &o, &candidate, 1, &binding); why != "" {
			t.Fatalf("candidate-owned package inventory refused: %s", why)
		}
	})
}

func TestWorkflowFootprintHarnessInventoryTamperRefuses(t *testing.T) {
	for _, count := range []int{32, 153} {
		for name, mutate := range map[string]func(*ProjectResolvedCoverage, *NativeOracleResult){
			"receipt count": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.HarnessInputCount++ },
			"receipt digest": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) {
				o.HarnessInputDigest = nativeDigest([]byte("tampered"))
			},
			"missing count":  func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.HarnessInputCount = 0; o.Files = nil },
			"missing digest": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.HarnessInputDigest = ""; o.Files = nil },
			"coverage path":  func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.Files[0].Path += ".changed" },
			"coverage blob":  func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.Files[0].BlobOID = strings.Repeat("e", 40) },
			"coverage file bytes": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
				b.Files[0].SHA256 = nativeDigest([]byte("changed file"))
			},
			"missing coverage input": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
				b.Files = b.Files[:len(b.Files)-1]
			},
			"extra coverage input": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
				b.Files = append(b.Files, NativeOracleFile{Path: "pkg/extra_test.go", BlobOID: strings.Repeat("f", 40), SHA256: nativeDigest([]byte("extra"))})
			},
			"unexpected receipt files": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) {
				o.Files = append(slices.Clone(o.Files), NativeOracleFile{Path: "pkg/foreign_test.go", BlobOID: strings.Repeat("f", 40), SHA256: nativeDigest([]byte("foreign"))})
			},
		} {
			t.Run(fmt.Sprintf("%d/%s", count, name), func(t *testing.T) {
				binding, o, candidate := footprintInventoryReceipt("go_top_level_v1", count)
				mutate(&binding, &o)
				if why := nativeOracleReceiptAnchorDeficiency("work", &o, &candidate, 1, &binding); why == "" {
					t.Fatal("tampered harness inventory qualified")
				}
			})
		}
	}
}

func TestWorkflowFootprintCompactInventoryKeepsProvenance(t *testing.T) {
	for name, mutate := range map[string]func(*ProjectResolvedCoverage, *NativeOracleResult){
		"recipe digest": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
			b.RecipeDigest = nativeDigest([]byte("foreign recipe"))
		},
		"source kind":    func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.RecipeSource.Kind = "knowledge" },
		"source project": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.RecipeSource.ProjectID = "other-project" },
		"source path": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
			b.RecipeSource.Path = ".concord/oracles/other.json"
		},
		"source commit": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) {
			b.RecipeSource.CommitOID = strings.Repeat("e", 40)
		},
		"case":           func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.CaseID = "case:other" },
		"case witnesses": func(b *ProjectResolvedCoverage, _ *NativeOracleResult) { b.Recipe.Witnesses = []string{"TestOther"} },
		"observed witnesses": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) {
			o.ObservedTestNames = nil
		},
		"family":   func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.RecipeKind = nativeTargetKind },
		"work":     func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.WorkID = "other-work" },
		"project":  func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.ProjectID = "other-project" },
		"subject":  func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.SubjectCommit = strings.Repeat("f", 40) },
		"contract": func(_ *ProjectResolvedCoverage, o *NativeOracleResult) { o.ContractVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			binding, o, candidate := footprintInventoryReceipt("go_top_level_v1", 153)
			mutate(&binding, &o)
			if why := nativeOracleReceiptAnchorDeficiency("work", &o, &candidate, 1, &binding); why == "" {
				t.Fatal("compact inventory bypassed source or provenance validation")
			}
		})
	}
}
