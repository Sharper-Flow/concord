package store

// Pure merge-tree parsing tests for the integration resolution value: the
// same `merge-tree --write-tree -z --no-messages` output always parses to
// the same sorted result or the same typed refusal. The tests bind the
// owner to the native NUL framing and exact field grammar of the pinned
// command, never to Git message prose. No Git process, store, caller path,
// or clock enters the parse.

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var (
	integrationTreeOID40  = strings.Repeat("a", 40)
	integrationTreeOID64  = strings.Repeat("b", 64)
	integrationStageOID40 = strings.Repeat("c", 40)
	integrationStageOID64 = strings.Repeat("d", 64)
	integrationOtherOID40 = strings.Repeat("e", 40)
	integrationZeroOID40  = strings.Repeat("0", 40)
)

// integrationMergeTreeRecord renders one `merge-tree --write-tree -z
// --no-messages` conflict record: "%06o <oid> <stage>\t<exact-path>" with
// its NUL terminator, the exact grammar the pinned options write.
func integrationMergeTreeRecord(mode, oid, stage, path string) string {
	return fmt.Sprintf("%s %s %s\t%s\x00", mode, oid, stage, path)
}

// integrationMergeTreeOutput renders the complete command stream: the tree
// OID, its NUL terminator, then each conflict record in the given order.
func integrationMergeTreeOutput(tree string, records ...string) []byte {
	return []byte(tree + "\x00" + strings.Join(records, ""))
}

// integrationRecordOIDFor returns a nonzero record OID sharing the tree's
// full object-format width, as every conflict record must.
func integrationRecordOIDFor(tree string) string {
	if len(tree) == 64 {
		return integrationStageOID64
	}
	return integrationStageOID40
}

// integrationConflictedRecords renders the canonical conflict record set in
// scrambled input order: three stages on one path, single-stage records on
// three more paths covering every index mode, and one spaced path.
func integrationConflictedRecords(oid string) []string {
	return []string{
		integrationMergeTreeRecord("100644", oid, "2", "internal/store/kept.go"),
		integrationMergeTreeRecord("100755", oid, "1", "internal/store/run.sh"),
		integrationMergeTreeRecord("120000", oid, "2", "internal/store/link"),
		integrationMergeTreeRecord("160000", oid, "3", "internal/store/sub"),
		integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go"),
		integrationMergeTreeRecord("100644", oid, "3", "internal/store/kept.go"),
		integrationMergeTreeRecord("100644", oid, "1", "dir with space/doc file.go"),
	}
}

// integrationConflictedStages wants the same record set sorted by exact
// path, then stage.
func integrationConflictedStages(oid string) []integrationConflictStage {
	return []integrationConflictStage{
		{Path: "dir with space/doc file.go", Mode: "100644", OID: oid, Stage: 1},
		{Path: "internal/store/kept.go", Mode: "100644", OID: oid, Stage: 1},
		{Path: "internal/store/kept.go", Mode: "100644", OID: oid, Stage: 2},
		{Path: "internal/store/kept.go", Mode: "100644", OID: oid, Stage: 3},
		{Path: "internal/store/link", Mode: "120000", OID: oid, Stage: 2},
		{Path: "internal/store/run.sh", Mode: "100755", OID: oid, Stage: 1},
		{Path: "internal/store/sub", Mode: "160000", OID: oid, Stage: 3},
	}
}

// integrationGeneratedStageSet builds one deterministic valid stage set
// from a seed at either hash width: unique (path, stage) pairs over a
// bounded varied path count, with per-record supported modes and varied
// stage subsets. It returns the tree, the records in shuffled render
// order, and the expected stages in canonical path-then-stage order built
// before the shuffle.
func integrationGeneratedStageSet(seed int64) (string, []string, []integrationConflictStage) {
	source := rand.New(rand.NewSource(seed))
	tree, oid := integrationTreeOID40, integrationStageOID40
	if source.Intn(2) == 1 {
		tree, oid = integrationTreeOID64, integrationStageOID64
	}
	modes := []string{"100644", "100755", "120000", "160000"}
	stages := []string{"1", "2", "3"}
	type stagePair struct {
		path     string
		mode     string
		stage    string
		stageNum int
	}
	pairs := make([]stagePair, 0, 24)
	for i := 0; i < 3+source.Intn(6); i++ {
		path := fmt.Sprintf("dir %d/generated-%02d/file.go", source.Intn(5), i)
		start := source.Intn(3)
		for k := 0; k < 1+source.Intn(3); k++ {
			stage := stages[(start+k)%3]
			pairs = append(pairs, stagePair{
				path:     path,
				mode:     modes[source.Intn(len(modes))],
				stage:    stage,
				stageNum: int(stage[0] - '0'),
			})
		}
	}
	sorted := make([]stagePair, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].path != sorted[j].path {
			return sorted[i].path < sorted[j].path
		}
		return sorted[i].stageNum < sorted[j].stageNum
	})
	want := make([]integrationConflictStage, 0, len(sorted))
	for _, pair := range sorted {
		want = append(want, integrationConflictStage{Path: pair.path, Mode: pair.mode, OID: oid, Stage: pair.stageNum})
	}
	records := make([]string, 0, len(pairs))
	for _, i := range source.Perm(len(pairs)) {
		records = append(records, integrationMergeTreeRecord(pairs[i].mode, oid, pairs[i].stage, pairs[i].path))
	}
	return tree, records, want
}

// assertIntegrationMergeTreeValid requires the parse to succeed and to
// carry the wanted tree, conflict flag, and stage records. An empty wanted
// stage list pins only the length, never the nil-versus-empty allocation.
func assertIntegrationMergeTreeValid(t *testing.T, got integrationMergeTreeResult, err error, wantTree string, wantConflicted bool, wantStages []integrationConflictStage) {
	t.Helper()
	if err != nil {
		t.Fatalf("valid merge-tree output refused: %v", err)
	}
	if got.TreeOID != wantTree {
		t.Fatalf("TreeOID = %q, want %q", got.TreeOID, wantTree)
	}
	if got.Conflicted != wantConflicted {
		t.Fatalf("Conflicted = %t, want %t", got.Conflicted, wantConflicted)
	}
	if wantStages == nil {
		if len(got.Conflicts) != 0 {
			t.Fatalf("Conflicts = %+v, want no stage records", got.Conflicts)
		}
		return
	}
	if !reflect.DeepEqual(got.Conflicts, wantStages) {
		t.Fatalf("Conflicts = %+v, want %+v", got.Conflicts, wantStages)
	}
}

// assertIntegrationMergeTreeRefusal requires a typed refusal whose text
// names at least one of the given markers, and the zero result value on
// error.
func assertIntegrationMergeTreeRefusal(t *testing.T, got integrationMergeTreeResult, err error, markers ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("parseIntegrationMergeTree accepted output it must refuse")
	}
	assertFailureKind(t, err, KindInvalidPayload)
	if !reflect.DeepEqual(got, integrationMergeTreeResult{}) {
		t.Fatalf("refused parse returned %+v, want the zero result", got)
	}
	for _, marker := range markers {
		if strings.Contains(err.Error(), marker) {
			return
		}
	}
	t.Fatalf("refusal %v names none of the markers %q", err, markers)
}

// integrationResolvedPath is the counted source path of the base
// resolution fixture, kept outside the merge-tree conflict paths so the
// two path sets never blur.
const integrationResolvedPath = "internal/store/resolved.go"

// integrationResolutionFixture composes one pinned merge-tree invocation
// beside the base T->tree(M) resolution slice: one counted source
// modification carrying three additions and two deletions, built from the
// S08 record builders.
func integrationResolutionFixture(exitCode int, records ...string) integrationResolutionDiffInput {
	return integrationResolutionDiffInput{
		MergeTreeExit:   exitCode,
		MergeTreeOutput: integrationMergeTreeOutput(integrationTreeOID40, records...),
		Resolution: sliceDiffInput{
			ProjectID: sliceDiffProjectID,
			RawDiff:   []byte(sliceDiffRawRecord("100644", "100644", "M", integrationResolvedPath)),
			Numstat:   []byte(sliceDiffNumstatRecord("3", "2", integrationResolvedPath)),
			OldBlobs:  map[string][]byte{integrationResolvedPath: sliceDiffTextBlob(2)},
			NewBlobs:  map[string][]byte{integrationResolvedPath: sliceDiffTextBlob(3)},
			Files: []WorkflowDesignArchitectureFile{
				{ProjectID: sliceDiffProjectID, Path: integrationResolvedPath, Role: DesignArchitectureFileRoleSource},
			},
			SliceFiles: []string{integrationResolvedPath},
			Limit:      designArchitectureMaxChangedLines,
		},
	}
}

// integrationResolutionVariant measures the composition fixture after the
// caller's mutation. The default stream is the canonical conflicted
// merge-tree output; a clean case drops it by overwriting MergeTreeOutput.
func integrationResolutionVariant(t *testing.T, exitCode int, change func(in *integrationResolutionDiffInput)) (integrationResolutionDiffResult, error) {
	t.Helper()
	oid := integrationRecordOIDFor(integrationTreeOID40)
	in := integrationResolutionFixture(exitCode, integrationConflictedRecords(oid)...)
	if change != nil {
		change(&in)
	}
	return measureIntegrationResolutionDiff(in)
}

// assertIntegrationResolutionRefusal requires a typed refusal whose text
// names at least one of the given markers, and the zero composition result
// on error, whatever stage refused: the merge-tree parse or the resolution
// measurement.
func assertIntegrationResolutionRefusal(t *testing.T, got integrationResolutionDiffResult, err error, markers ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("measureIntegrationResolutionDiff accepted input it must refuse")
	}
	assertFailureKind(t, err, KindInvalidPayload)
	if !reflect.DeepEqual(got, integrationResolutionDiffResult{}) {
		t.Fatalf("refused composition returned %+v, want the zero result", got)
	}
	for _, marker := range markers {
		if strings.Contains(err.Error(), marker) {
			return
		}
	}
	t.Fatalf("refusal %v names none of the markers %q", err, markers)
}

func TestIntegrationResolutionDiff(t *testing.T) {
	t.Run("merge_tree accepts clean exit0 with the tree only", func(t *testing.T) {
		out := integrationMergeTreeOutput(integrationTreeOID40)
		got, err := parseIntegrationMergeTree(0, out)
		assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, false, nil)
		again, err := parseIntegrationMergeTree(0, out)
		assertIntegrationMergeTreeValid(t, again, err, integrationTreeOID40, false, nil)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("same output parsed %+v then %+v; the parser is not deterministic", got, again)
		}
	})

	t.Run("merge_tree accepts exit1 with no conflict records as conflicted", func(t *testing.T) {
		// A conflicted merge can carry zero stage records: the exit status
		// alone derives Conflicted, never the record list.
		got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40))
		assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, nil)
	})

	t.Run("merge_tree accepts exit1 conflict records sorted by path then stage", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationConflictedRecords(oid)...))
		assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, integrationConflictedStages(oid))
	})

	t.Run("merge_tree accepts the 64-hex object format", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID64)
		got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID64, integrationConflictedRecords(oid)...))
		assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID64, true, integrationConflictedStages(oid))
	})

	t.Run("merge_tree refuses exit codes outside 0 and 1", func(t *testing.T) {
		for _, exitCode := range []int{2, 128, -1} {
			got, err := parseIntegrationMergeTree(exitCode, integrationMergeTreeOutput(integrationTreeOID40))
			assertIntegrationMergeTreeRefusal(t, got, err, "exit")
		}
	})

	t.Run("merge_tree refuses conflict records under a clean exit status", func(t *testing.T) {
		// Exit0 writes the tree only: the grammar is exit-status-dependent,
		// so records under a clean status are not the pinned command's
		// output and cannot be absorbed as informational.
		oid := integrationRecordOIDFor(integrationTreeOID40)
		out := integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go"))
		got, err := parseIntegrationMergeTree(0, out)
		assertIntegrationMergeTreeRefusal(t, got, err, "exit")
	})

	t.Run("merge_tree validates the tree OID", func(t *testing.T) {
		// Each row names the exact constructed stream bytes.
		cases := []struct {
			name string
			tree string
		}{
			{"terminator only without a tree", ""},
			{"terminator plus an empty record", "\x00"},
			{"all-zero tree", integrationZeroOID40},
			{"uppercase tree", strings.Repeat("A", 40)},
			{"non-hex tree", strings.Repeat("z", 40)},
			{"39 digits", strings.Repeat("a", 39)},
			{"41 digits", strings.Repeat("a", 41)},
			{"63 digits", strings.Repeat("a", 63)},
			{"65 digits", strings.Repeat("a", 65)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(0, integrationMergeTreeOutput(tc.tree))
				assertIntegrationMergeTreeRefusal(t, got, err, "OID", "malformed")
			})
		}

		t.Run("nil output refuses with no tree OID", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(0, nil)
			assertIntegrationMergeTreeRefusal(t, got, err, "no tree OID")
		})

		t.Run("empty output refuses with no tree OID", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(0, []byte{})
			assertIntegrationMergeTreeRefusal(t, got, err, "no tree OID")
		})
	})

	t.Run("merge_tree enforces complete NUL framing", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		record := integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go")

		t.Run("clean stream without its final NUL refuses", func(t *testing.T) {
			out := integrationMergeTreeOutput(integrationTreeOID40)
			got, err := parseIntegrationMergeTree(0, out[:len(out)-1])
			assertIntegrationMergeTreeRefusal(t, got, err, "malformed")
		})

		t.Run("record stream without its final NUL refuses", func(t *testing.T) {
			out := integrationMergeTreeOutput(integrationTreeOID40, record)
			got, err := parseIntegrationMergeTree(1, out[:len(out)-1])
			assertIntegrationMergeTreeRefusal(t, got, err, "malformed")
		})

		t.Run("record truncated inside its path refuses", func(t *testing.T) {
			out := integrationMergeTreeOutput(integrationTreeOID40, record[:len(record)-3])
			got, err := parseIntegrationMergeTree(1, out)
			assertIntegrationMergeTreeRefusal(t, got, err, "malformed")
		})

		t.Run("extra NUL terminator refuses", func(t *testing.T) {
			// With --no-messages there is no blank or NUL terminator after
			// the last record: a trailing empty record is not native output.
			out := append(integrationMergeTreeOutput(integrationTreeOID40, record), 0)
			got, err := parseIntegrationMergeTree(1, out)
			assertIntegrationMergeTreeRefusal(t, got, err, "empty", "malformed")
		})

		t.Run("extra message record refuses", func(t *testing.T) {
			out := integrationMergeTreeOutput(integrationTreeOID40, record, "warning: conflict messages are refused\x00")
			got, err := parseIntegrationMergeTree(1, out)
			assertIntegrationMergeTreeRefusal(t, got, err, "malformed")
		})

		t.Run("records without a leading tree OID refuse", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, []byte(record))
			assertIntegrationMergeTreeRefusal(t, got, err, "OID", "malformed")
		})
	})

	t.Run("merge_tree refuses output beyond the 4 MiB limit", func(t *testing.T) {
		// The stream is otherwise valid: every record is well-formed and its
		// 380-byte path carries a unique numeric suffix inside the shared
		// path vocabulary, so no record refuses and the aggregate size is
		// the stream's only defect. The refusal must name the limit, not
		// the framing.
		var b strings.Builder
		b.WriteString(integrationTreeOID40)
		b.WriteString("\x00")
		for i := 0; i < 10000; i++ {
			path := "internal/store/records/" + strings.Repeat("a", 347) + fmt.Sprintf("%07d", i) + ".go"
			b.WriteString(integrationMergeTreeRecord("100644", integrationStageOID40, "1", path))
		}
		got, err := parseIntegrationMergeTree(1, []byte(b.String()))
		assertIntegrationMergeTreeRefusal(t, got, err, "limit", "exceeds")
	})

	t.Run("merge_tree parses the exact record grammar", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		cases := []struct {
			name    string
			record  string
			markers []string
		}{
			{"name-only record without a header", "internal/store/kept.go\x00", []string{"malformed"}},
			{"header without a stage field", "100644 " + oid + "\tinternal/store/kept.go\x00", []string{"malformed"}},
			{"header with an extra field", "100644 " + oid + " 1 extra\tinternal/store/kept.go\x00", []string{"malformed"}},
			{"space instead of tab before the path", "100644 " + oid + " 1 internal/store/kept.go\x00", []string{"malformed"}},
			{"tab instead of space between mode and OID", "100644\t" + oid + " 1\tinternal/store/kept.go\x00", []string{"malformed", "control"}},
			{"double space between mode and OID", "100644  " + oid + " 1\tinternal/store/kept.go\x00", []string{"malformed", "OID"}},
			{"trailing space after the stage", "100644 " + oid + " 1 \tinternal/store/kept.go\x00", []string{"stage", "malformed"}},
			{"header without a following path", "100644 " + oid + " 1\x00", []string{"malformed"}},
			{"empty path", "100644 " + oid + " 1\t\x00", []string{"path", "malformed"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, tc.record))
				assertIntegrationMergeTreeRefusal(t, got, err, tc.markers...)
			})
		}
	})

	t.Run("merge_tree accepts only git index-file modes", func(t *testing.T) {
		const path = "internal/store/kept.go"
		oid := integrationRecordOIDFor(integrationTreeOID40)
		for _, mode := range []string{"100644", "100755", "120000", "160000"} {
			t.Run("accepts "+mode, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord(mode, oid, "1", path)))
				assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, []integrationConflictStage{{Path: path, Mode: mode, OID: oid, Stage: 1}})
			})
		}
		for _, mode := range []string{"040000", "100664", "000000", "1006445", "10064", "1006ab"} {
			t.Run("refuses "+mode, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord(mode, oid, "1", path)))
				assertIntegrationMergeTreeRefusal(t, got, err, "mode")
			})
		}
	})

	t.Run("merge_tree accepts only conflict stages 1, 2, and 3", func(t *testing.T) {
		const path = "internal/store/kept.go"
		oid := integrationRecordOIDFor(integrationTreeOID40)
		cases := []struct {
			name    string
			stage   string
			markers []string
		}{
			{"stage 0", "0", []string{"stage"}},
			{"stage 4", "4", []string{"stage"}},
			{"stage 12", "12", []string{"stage"}},
			{"negative stage", "-1", []string{"stage"}},
			{"non-numeric stage", "x", []string{"stage"}},
			{"empty stage", "", []string{"stage", "malformed"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", oid, tc.stage, path)))
				assertIntegrationMergeTreeRefusal(t, got, err, tc.markers...)
			})
		}
	})

	t.Run("merge_tree requires nonzero hexadecimal record OIDs", func(t *testing.T) {
		const path = "internal/store/kept.go"
		cases := []struct {
			name string
			oid  string
		}{
			{"all-zero OID", integrationZeroOID40},
			{"non-hex OID", strings.Repeat("g", 40)},
			{"uppercase OID", strings.Repeat("C", 40)},
			{"short OID", strings.Repeat("a", 20)},
			{"long OID", strings.Repeat("a", 41)},
			{"empty OID", ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", tc.oid, "1", path)))
				assertIntegrationMergeTreeRefusal(t, got, err, "OID", "malformed")
			})
		}
	})

	t.Run("merge_tree refuses record OIDs that mismatch the tree hash width", func(t *testing.T) {
		const path = "internal/store/kept.go"
		t.Run("40-digit tree with a 64-digit record OID", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", integrationStageOID64, "1", path)))
			assertIntegrationMergeTreeRefusal(t, got, err, "OID")
		})

		t.Run("64-digit tree with a 40-digit record OID", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID64, integrationMergeTreeRecord("100644", integrationStageOID40, "1", path)))
			assertIntegrationMergeTreeRefusal(t, got, err, "OID")
		})
	})

	t.Run("merge_tree refuses a duplicate path and stage", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)

		t.Run("identical records refuse even when not adjacent", func(t *testing.T) {
			records := []string{
				integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go"),
				integrationMergeTreeRecord("100755", oid, "1", "internal/store/run.sh"),
				integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go"),
			}
			got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, records...))
			assertIntegrationMergeTreeRefusal(t, got, err, "duplicate")
		})

		t.Run("same path and stage with different OIDs refuse", func(t *testing.T) {
			records := []string{
				integrationMergeTreeRecord("100644", oid, "1", "internal/store/kept.go"),
				integrationMergeTreeRecord("100644", integrationOtherOID40, "1", "internal/store/kept.go"),
			}
			got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, records...))
			assertIntegrationMergeTreeRefusal(t, got, err, "duplicate")
		})
	})

	t.Run("merge_tree applies the exact path vocabulary", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		output := func(path string) []byte {
			return integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", oid, "1", path))
		}

		t.Run("space in a path is valid", func(t *testing.T) {
			const path = "dir with space/doc file.go"
			got, err := parseIntegrationMergeTree(1, output(path))
			assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, []integrationConflictStage{{Path: path, Mode: "100644", OID: oid, Stage: 1}})
		})

		t.Run("invalid UTF-8 path bytes refuse", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, output("internal/store/bad\xff\xfename.go"))
			assertIntegrationMergeTreeRefusal(t, got, err, "UTF-8")
		})

		t.Run("tab in a path refuses with the bytes intact", func(t *testing.T) {
			tabPath := "dir\tfile.go"
			got, err := parseIntegrationMergeTree(1, output(tabPath))
			assertIntegrationMergeTreeRefusal(t, got, err, "control")
			// The refusal quotes the path with %q, so the escaped form proves
			// the complete path survived parsing without a split at the
			// control byte and without normalization into acceptance.
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tabPath)) {
				t.Fatalf("refusal %v does not carry the quoted full path %q; the parser split the path", err, fmt.Sprintf("%q", tabPath))
			}
		})

		t.Run("newline in a path refuses with the bytes intact", func(t *testing.T) {
			newlinePath := "dir\nfile.go"
			got, err := parseIntegrationMergeTree(1, output(newlinePath))
			assertIntegrationMergeTreeRefusal(t, got, err, "control")
			// NUL framing alone delimits records: the newline must arrive
			// inside the path token and refuse as a control byte, never act
			// as a record or field separator.
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", newlinePath)) {
				t.Fatalf("refusal %v does not carry the quoted full path %q; the parser split the path", err, fmt.Sprintf("%q", newlinePath))
			}
		})

		t.Run("delete byte in a path refuses", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, output("internal/store/x\x7fy.go"))
			assertIntegrationMergeTreeRefusal(t, got, err, "control")
		})

		t.Run("unsafe path shapes refuse", func(t *testing.T) {
			cases := []struct {
				name   string
				path   string
				marker string
			}{
				{"parent traversal", "../escape.go", "normalized"},
				{"absolute path", "/etc/passwd", "normalized"},
				{"trailing separator", "internal/store/", "normalized"},
				{"empty segment", "internal//store.go", "normalized"},
				{"self segment", "internal/./store.go", "normalized"},
				{"glob spelling", "internal/*.go", "glob"},
				{"bracket glob", "internal/store/[ab].go", "glob"},
				{"tmp first segment", "tmp/scratch.go", "tmp"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					got, err := parseIntegrationMergeTree(1, output(tc.path))
					assertIntegrationMergeTreeRefusal(t, got, err, tc.marker)
				})
			}
		})

		t.Run("valid multibyte UTF-8 path bytes survive exact", func(t *testing.T) {
			const path = "internal/store/国際化.go"
			got, err := parseIntegrationMergeTree(1, output(path))
			assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, []integrationConflictStage{{Path: path, Mode: "100644", OID: oid, Stage: 1}})
		})
	})

	t.Run("merge_tree stage records stay structurally distinct from resolution scope", func(t *testing.T) {
		// The automatic-tree value carries only the parse of the pinned
		// command: tree identity, exit-derived conflict flag, and verbatim
		// stage records. No resolution role, cost, or authority field exists
		// on it; the resolution result is composed beside it, never inside.
		resultType := reflect.TypeOf(integrationMergeTreeResult{})
		resultFields := make([]string, 0, resultType.NumField())
		for i := 0; i < resultType.NumField(); i++ {
			resultFields = append(resultFields, resultType.Field(i).Name)
		}
		if !reflect.DeepEqual(resultFields, []string{"TreeOID", "Conflicted", "Conflicts"}) {
			t.Fatalf("integrationMergeTreeResult carries fields %v, want exactly TreeOID, Conflicted, and Conflicts", resultFields)
		}
		stageType := reflect.TypeOf(integrationConflictStage{})
		stageFields := make([]string, 0, stageType.NumField())
		for i := 0; i < stageType.NumField(); i++ {
			stageFields = append(stageFields, stageType.Field(i).Name)
		}
		if !reflect.DeepEqual(stageFields, []string{"Path", "Mode", "OID", "Stage"}) {
			t.Fatalf("integrationConflictStage carries fields %v, want exactly Path, Mode, OID, and Stage", stageFields)
		}
		conflictsType := resultType.Field(2).Type
		if conflictsType.Kind() != reflect.Slice || conflictsType.Elem() != stageType {
			t.Fatalf("Conflicts field type is %s, want a slice of %s", conflictsType, stageType)
		}
	})

	t.Run("merge_tree stage order is deterministic under record permutation", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		records := []string{
			integrationMergeTreeRecord("100644", oid, "2", "internal/store/alpha.go"),
			integrationMergeTreeRecord("100644", oid, "1", "internal/store/alpha.go"),
			integrationMergeTreeRecord("100644", oid, "3", "internal/store/alpha.go"),
			integrationMergeTreeRecord("100755", oid, "1", "dir with space/beta file.go"),
			integrationMergeTreeRecord("120000", oid, "2", "internal/store/国際化.go"),
			integrationMergeTreeRecord("160000", oid, "3", "internal/store/omega.go"),
		}
		want := []integrationConflictStage{
			{Path: "dir with space/beta file.go", Mode: "100755", OID: oid, Stage: 1},
			{Path: "internal/store/alpha.go", Mode: "100644", OID: oid, Stage: 1},
			{Path: "internal/store/alpha.go", Mode: "100644", OID: oid, Stage: 2},
			{Path: "internal/store/alpha.go", Mode: "100644", OID: oid, Stage: 3},
			{Path: "internal/store/omega.go", Mode: "160000", OID: oid, Stage: 3},
			{Path: "internal/store/国際化.go", Mode: "120000", OID: oid, Stage: 2},
		}

		baseline, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, records...))
		assertIntegrationMergeTreeValid(t, baseline, err, integrationTreeOID40, true, want)

		reverse := func(n int) []int {
			order := make([]int, n)
			for i := range order {
				order[i] = n - 1 - i
			}
			return order
		}
		rotate := func(n, k int) []int {
			order := make([]int, n)
			for i := range order {
				order[i] = (i + k) % n
			}
			return order
		}
		shuffleSource := rand.New(rand.NewSource(9))
		permutations := []struct {
			name  string
			order []int
		}{
			{"identity", rotate(len(records), 0)},
			{"reversed", reverse(len(records))},
			{"rotated by one", rotate(len(records), 1)},
			{"rotated by four", rotate(len(records), 4)},
			{"seeded shuffle 1", shuffleSource.Perm(len(records))},
			{"seeded shuffle 2", shuffleSource.Perm(len(records))},
			{"seeded shuffle 3", shuffleSource.Perm(len(records))},
		}

		for _, perm := range permutations {
			t.Run(perm.name, func(t *testing.T) {
				ordered := make([]string, 0, len(records))
				for _, i := range perm.order {
					ordered = append(ordered, records[i])
				}
				got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, ordered...))
				if err != nil {
					t.Fatalf("reordered parse refused: %v", err)
				}
				if got.TreeOID != integrationTreeOID40 || !got.Conflicted {
					t.Fatalf("reordered parse = %+v, want the same tree and conflicted flag", got)
				}
				if !reflect.DeepEqual(got.Conflicts, want) {
					t.Fatalf("reordered conflicts = %+v, want the deterministic %+v", got.Conflicts, want)
				}
				if !sort.SliceIsSorted(got.Conflicts, func(i, j int) bool {
					if got.Conflicts[i].Path != got.Conflicts[j].Path {
						return got.Conflicts[i].Path < got.Conflicts[j].Path
					}
					return got.Conflicts[i].Stage < got.Conflicts[j].Stage
				}) {
					t.Fatalf("conflicts are not sorted by path then stage: %+v", got.Conflicts)
				}
			})
		}
	})

	t.Run("merge_tree generated stage sets stay sorted under permutation", func(t *testing.T) {
		for _, seed := range []int64{1, 2, 3, 4, 5} {
			t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
				tree, records, want := integrationGeneratedStageSet(seed)
				shuffled, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(tree, records...))
				assertIntegrationMergeTreeValid(t, shuffled, err, tree, true, want)
				reversed := make([]string, len(records))
				for i, record := range records {
					reversed[len(records)-1-i] = record
				}
				permuted, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(tree, reversed...))
				assertIntegrationMergeTreeValid(t, permuted, err, tree, true, want)
				if !reflect.DeepEqual(shuffled.Conflicts, permuted.Conflicts) {
					t.Fatalf("shuffled and reversed parses differ: %+v then %+v", shuffled.Conflicts, permuted.Conflicts)
				}
			})
		}
	})

	t.Run("resolution composes a conflicted tree and its resolution edits with distinct attribution", func(t *testing.T) {
		// The automatic tree keeps its parse and the resolution keeps its
		// measurement in separate fields: neither attribution absorbs the
		// other, and the same input composes to the same value twice.
		oid := integrationRecordOIDFor(integrationTreeOID40)
		got, err := integrationResolutionVariant(t, 1, nil)
		if err != nil {
			t.Fatalf("valid composition refused: %v", err)
		}
		assertIntegrationMergeTreeValid(t, got.AutomaticTree, err, integrationTreeOID40, true, integrationConflictedStages(oid))
		want := sliceDiffResult{
			Paths:      []slicePathCounts{{Path: integrationResolvedPath, Role: DesignArchitectureFileRoleSource, Additions: 3, Deletions: 2}},
			TotalLines: 5,
		}
		if !reflect.DeepEqual(got.Resolution, want) {
			t.Fatalf("Resolution = %+v, want %+v", got.Resolution, want)
		}
		again, err := integrationResolutionVariant(t, 1, nil)
		if err != nil {
			t.Fatalf("second identical composition refused: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("same input composed %+v then %+v; the composition is not deterministic", got, again)
		}
	})

	t.Run("resolution charges a clean merge's extra resolution edits", func(t *testing.T) {
		// Exit0 means a clean automatic tree, never a free resolution: the
		// composition must still measure every supplied T->tree(M) edit.
		got, err := integrationResolutionVariant(t, 0, func(in *integrationResolutionDiffInput) {
			in.MergeTreeOutput = integrationMergeTreeOutput(integrationTreeOID40)
		})
		if err != nil {
			t.Fatalf("clean-merge composition refused: %v", err)
		}
		assertIntegrationMergeTreeValid(t, got.AutomaticTree, err, integrationTreeOID40, false, nil)
		if len(got.Resolution.Paths) != 1 || got.Resolution.TotalLines != 5 {
			t.Fatalf("Resolution = %+v, want the supplied edit charged at 5 lines", got.Resolution)
		}
	})

	t.Run("resolution keeps a genuine zero cost when conflict paths stay outside the slice", func(t *testing.T) {
		// Conflict records describe the automatic tree only: with no
		// resolution edits supplied, the composition measures zero and the
		// conflict paths never enter the resolution result.
		got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
			in.Resolution = sliceDiffInput{ProjectID: sliceDiffProjectID, Limit: designArchitectureMaxChangedLines}
		})
		if err != nil {
			t.Fatalf("automatic-only composition refused: %v", err)
		}
		if !got.AutomaticTree.Conflicted || len(got.AutomaticTree.Conflicts) == 0 {
			t.Fatalf("AutomaticTree = %+v, want the conflicted parse with its stage records", got.AutomaticTree)
		}
		if len(got.Resolution.Paths) != 0 || got.Resolution.TotalLines != 0 {
			t.Fatalf("Resolution = %+v, want zero paths and a zero total", got.Resolution)
		}
	})

	t.Run("resolution keeps the protected-path scope checks on the delta", func(t *testing.T) {
		// The automatic conflict set grants no scope: every delta path must
		// still match the resolution slice set, its approved map, and its
		// unchanged list exactly as S08 requires.
		const conflictPath = "internal/store/kept.go"
		extendDelta := func(in *integrationResolutionDiffInput) {
			in.Resolution.RawDiff = []byte(strings.Join([]string{
				sliceDiffRawRecord("100644", "100644", "M", integrationResolvedPath),
				sliceDiffRawRecord("100644", "100644", "M", conflictPath),
			}, ""))
			in.Resolution.Numstat = []byte(strings.Join([]string{
				sliceDiffNumstatRecord("3", "2", integrationResolvedPath),
				sliceDiffNumstatRecord("1", "1", conflictPath),
			}, ""))
			in.Resolution.OldBlobs[conflictPath] = sliceDiffTextBlob(1)
			in.Resolution.NewBlobs[conflictPath] = sliceDiffTextBlob(1)
		}

		t.Run("conflict path outside the slice set refuses despite its record", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				extendDelta(in)
				in.Resolution.Files = append(in.Resolution.Files, WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: conflictPath, Role: DesignArchitectureFileRoleSource})
			})
			assertIntegrationResolutionRefusal(t, got, err, "slice")
		})

		t.Run("delta path absent from the approved file map refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				extendDelta(in)
				in.Resolution.SliceFiles = append(in.Resolution.SliceFiles, conflictPath)
			})
			assertIntegrationResolutionRefusal(t, got, err, "role")
		})

		t.Run("changed path declared unchanged refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.Resolution.Unchanged = []string{integrationResolvedPath}
			})
			assertIntegrationResolutionRefusal(t, got, err, "unchanged")
		})
	})

	t.Run("resolution conflict stage modes neither grant nor refuse permission", func(t *testing.T) {
		// The conflict record carries mode 120000 on the measured path while
		// the resolution is an ordinary regular-file source edit: only the
		// resolution slice, map, and streams decide the outcome.
		record := integrationMergeTreeRecord("120000", integrationRecordOIDFor(integrationTreeOID40), "2", integrationResolvedPath)
		got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
			in.MergeTreeOutput = integrationMergeTreeOutput(integrationTreeOID40, record)
		})
		if err != nil {
			t.Fatalf("composition refused a resolution its own inputs approve: %v", err)
		}
		if got.Resolution.TotalLines != 5 || len(got.Resolution.Paths) != 1 {
			t.Fatalf("Resolution = %+v, want the measured source edit", got.Resolution)
		}
	})

	t.Run("resolution keeps role exclusions without a scope exemption", func(t *testing.T) {
		t.Run("test and generated paths measure as excluded zero", func(t *testing.T) {
			const (
				testPath      = "internal/store/resolved_test.go"
				generatedPath = "assets/resolved.min.js"
			)
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.Resolution.RawDiff = []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", integrationResolvedPath),
					sliceDiffRawRecord("100644", "100644", "M", testPath),
					sliceDiffRawRecord("100644", "100644", "M", generatedPath),
				}, ""))
				in.Resolution.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("3", "2", integrationResolvedPath),
					sliceDiffNumstatRecord("-", "-", testPath),
					sliceDiffNumstatRecord("-", "-", generatedPath),
				}, ""))
				in.Resolution.Files = append(in.Resolution.Files,
					WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: testPath, Role: DesignArchitectureFileRoleTest},
					WorkflowDesignArchitectureFile{ProjectID: sliceDiffProjectID, Path: generatedPath, Role: DesignArchitectureFileRoleGenerated},
				)
				in.Resolution.SliceFiles = append(in.Resolution.SliceFiles, testPath, generatedPath)
			})
			if err != nil {
				t.Fatalf("excluded composition refused: %v", err)
			}
			want := sliceDiffResult{
				Paths: []slicePathCounts{
					{Path: generatedPath, Role: DesignArchitectureFileRoleGenerated},
					{Path: integrationResolvedPath, Role: DesignArchitectureFileRoleSource, Additions: 3, Deletions: 2},
					{Path: testPath, Role: DesignArchitectureFileRoleTest},
				},
				TotalLines: 5,
			}
			if !reflect.DeepEqual(got.Resolution, want) {
				t.Fatalf("Resolution = %+v, want %+v", got.Resolution, want)
			}
		})

		t.Run("generated-sounding name without a map entry refuses", func(t *testing.T) {
			// No role inference and no scope exemption: an excluded-sounding
			// name still needs its own approved map entry.
			const exemptPath = "internal/store/generated_helpers.go"
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.Resolution.RawDiff = []byte(strings.Join([]string{
					sliceDiffRawRecord("100644", "100644", "M", integrationResolvedPath),
					sliceDiffRawRecord("100644", "100644", "M", exemptPath),
				}, ""))
				in.Resolution.Numstat = []byte(strings.Join([]string{
					sliceDiffNumstatRecord("3", "2", integrationResolvedPath),
					sliceDiffNumstatRecord("1", "1", exemptPath),
				}, ""))
				in.Resolution.SliceFiles = append(in.Resolution.SliceFiles, exemptPath)
				in.Resolution.OldBlobs[exemptPath] = sliceDiffTextBlob(1)
				in.Resolution.NewBlobs[exemptPath] = sliceDiffTextBlob(1)
			})
			assertIntegrationResolutionRefusal(t, got, err, "role")
		})
	})

	t.Run("resolution accepts exactly 500 counted lines and refuses 501", func(t *testing.T) {
		replacement := func(additions, deletions string) func(in *integrationResolutionDiffInput) {
			return func(in *integrationResolutionDiffInput) {
				in.Resolution.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", integrationResolvedPath))
				in.Resolution.Numstat = []byte(sliceDiffNumstatRecord(additions, deletions, integrationResolvedPath))
			}
		}

		t.Run("250 plus 250 counts 500 at the declared limit", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, replacement("250", "250"))
			if err != nil {
				t.Fatalf("limit-boundary replacement refused: %v", err)
			}
			if got.Resolution.TotalLines != 500 {
				t.Fatalf("Resolution total = %d, want 500 and never the net 0", got.Resolution.TotalLines)
			}
		})

		t.Run("501 counted lines refuse above the limit", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, replacement("251", "250"))
			assertIntegrationResolutionRefusal(t, got, err, "limit")
		})
	})

	t.Run("resolution charges rename deletion and addition endpoints, each scoped", func(t *testing.T) {
		const (
			oldName = "internal/store/resolved_old.go"
			newName = "internal/store/resolved_new.go"
		)
		rename := func(in *integrationResolutionDiffInput) {
			in.Resolution.RawDiff = []byte(strings.Join([]string{
				sliceDiffRawRecord("100644", "000000", "D", oldName),
				sliceDiffRawRecord("000000", "100644", "A", newName),
			}, ""))
			in.Resolution.Numstat = []byte(strings.Join([]string{
				sliceDiffNumstatRecord("0", "12", oldName),
				sliceDiffNumstatRecord("34", "0", newName),
			}, ""))
			in.Resolution.OldBlobs = map[string][]byte{oldName: sliceDiffTextBlob(2)}
			in.Resolution.NewBlobs = map[string][]byte{newName: sliceDiffTextBlob(3)}
			in.Resolution.Files = []WorkflowDesignArchitectureFile{
				{ProjectID: sliceDiffProjectID, Path: oldName, Role: DesignArchitectureFileRoleSource},
				{ProjectID: sliceDiffProjectID, Path: newName, Role: DesignArchitectureFileRoleSource},
			}
			in.Resolution.SliceFiles = []string{oldName, newName}
		}

		t.Run("both endpoints count when both are scoped", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, rename)
			if err != nil {
				t.Fatalf("scoped rename refused: %v", err)
			}
			want := sliceDiffResult{
				Paths: []slicePathCounts{
					{Path: newName, Role: DesignArchitectureFileRoleSource, Additions: 34},
					{Path: oldName, Role: DesignArchitectureFileRoleSource, Deletions: 12},
				},
				TotalLines: 46,
			}
			if !reflect.DeepEqual(got.Resolution, want) {
				t.Fatalf("Resolution = %+v, want %+v", got.Resolution, want)
			}
		})

		t.Run("new endpoint without an approved role refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				rename(in)
				in.Resolution.Files = in.Resolution.Files[:1]
			})
			assertIntegrationResolutionRefusal(t, got, err, "role")
		})

		t.Run("old endpoint outside the slice set refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				rename(in)
				in.Resolution.SliceFiles = []string{newName}
			})
			assertIntegrationResolutionRefusal(t, got, err, "slice")
		})
	})

	t.Run("resolution refuses binary and unsupported-mode source resolutions", func(t *testing.T) {
		t.Run("binary numstat on a counted source path refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.Resolution.Numstat = []byte(sliceDiffNumstatRecord("-", "-", integrationResolvedPath))
			})
			assertIntegrationResolutionRefusal(t, got, err, "binary")
		})

		t.Run("symlink resolution mode refuses rather than becoming free", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.Resolution.RawDiff = []byte(sliceDiffRawRecord("000000", "120000", "A", integrationResolvedPath))
				in.Resolution.Numstat = []byte{}
			})
			assertIntegrationResolutionRefusal(t, got, err, "mode")
		})
	})

	t.Run("resolution refuses a malformed merge-tree stream before measuring", func(t *testing.T) {
		// The merge-tree parse runs first: an otherwise-valid resolution
		// slice is never measured when the automatic tree refuses, and every
		// refusal returns the zero composition result.

		t.Run("truncated framing with a valid resolution refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
				in.MergeTreeOutput = in.MergeTreeOutput[:len(in.MergeTreeOutput)-1]
			})
			assertIntegrationResolutionRefusal(t, got, err, "malformed")
		})

		t.Run("exit outside the 0 and 1 pair with a valid resolution refuses", func(t *testing.T) {
			got, err := integrationResolutionVariant(t, 2, nil)
			assertIntegrationResolutionRefusal(t, got, err, "exit")
		})

		t.Run("invalid exit beside an invalid resolution refuses from the parse stage", func(t *testing.T) {
			// Both stages are invalid, so the refusal text proves which one
			// ran first: the exit-specific parse refusal must win over the
			// resolution limit refusal, and the zero result must return.
			got, err := integrationResolutionVariant(t, 2, func(in *integrationResolutionDiffInput) {
				in.Resolution.Limit = 0
			})
			assertIntegrationResolutionRefusal(t, got, err, "exit status")
		})
	})

	t.Run("resolution keeps the composition types to their declared fields", func(t *testing.T) {
		// The input carries exactly one resolution stream set, so the
		// composition can only measure T->tree(M) against it: no A->T streams
		// exist to measure. The result keeps the automatic tree and the
		// resolution measurement in separate fields with their own types.
		inputType := reflect.TypeOf(integrationResolutionDiffInput{})
		inputFields := make([]string, 0, inputType.NumField())
		for i := 0; i < inputType.NumField(); i++ {
			inputFields = append(inputFields, inputType.Field(i).Name)
		}
		if !reflect.DeepEqual(inputFields, []string{"MergeTreeExit", "MergeTreeOutput", "Resolution"}) {
			t.Fatalf("integrationResolutionDiffInput carries fields %v, want exactly MergeTreeExit, MergeTreeOutput, and Resolution", inputFields)
		}
		if inputType.Field(2).Type != reflect.TypeOf(sliceDiffInput{}) {
			t.Fatalf("Resolution input field type is %s, want %s", inputType.Field(2).Type, reflect.TypeOf(sliceDiffInput{}))
		}
		resultType := reflect.TypeOf(integrationResolutionDiffResult{})
		resultFields := make([]string, 0, resultType.NumField())
		for i := 0; i < resultType.NumField(); i++ {
			resultFields = append(resultFields, resultType.Field(i).Name)
		}
		if !reflect.DeepEqual(resultFields, []string{"AutomaticTree", "Resolution"}) {
			t.Fatalf("integrationResolutionDiffResult carries fields %v, want exactly AutomaticTree and Resolution", resultFields)
		}
		if resultType.Field(0).Type != reflect.TypeOf(integrationMergeTreeResult{}) {
			t.Fatalf("AutomaticTree field type is %s, want %s", resultType.Field(0).Type, reflect.TypeOf(integrationMergeTreeResult{}))
		}
		if resultType.Field(1).Type != reflect.TypeOf(sliceDiffResult{}) {
			t.Fatalf("Resolution result field type is %s, want %s", resultType.Field(1).Type, reflect.TypeOf(sliceDiffResult{}))
		}
	})
}
