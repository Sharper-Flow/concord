package store

// Pure integration merge-tree value: the same `merge-tree --write-tree -z
// --no-messages` output always parses to the same sorted result or the
// same typed refusal. No Git process, store, caller path, or clock enters
// the parse.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// integrationConflictStage is one native conflict record: the exact path
// bytes, the index-file mode, the record object name, and the conflict
// stage. It describes the automatic tree only and carries no resolution
// scope.
type integrationConflictStage struct {
	Path  string
	Mode  string
	OID   string
	Stage int
}

// integrationMergeTreeResult is the validated parse of one pinned
// merge-tree invocation: the written tree identity, the exit-derived
// conflict flag, and the conflict records sorted by exact path, then
// stage.
type integrationMergeTreeResult struct {
	TreeOID    string
	Conflicted bool
	Conflicts  []integrationConflictStage
}

func integrationMergeTreeFailure(detail, recovery string) error {
	return newFailure(KindInvalidPayload, "worker_integration_diff", detail, false, recovery)
}

// integrationConflictMode reports whether the mode is one of the Git
// index-file modes the pinned command writes: the regular-file modes
// shared with slice diff, plus the symlink and gitlink modes.
func integrationConflictMode(mode string) bool {
	switch mode {
	case sliceDiffModeRegular, sliceDiffModeExecutable, "120000", "160000":
		return true
	}
	return false
}

// integrationObjectOID reports the syntactic shape only: a full lowercase
// 40/64-digit hex object name that is not the all-zero name. It is not
// proof that the object exists; the all-zero shape refuses because a
// written tree is never that name.
func integrationObjectOID(value string) bool {
	if !isWorktreeOID(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] != '0' {
			return true
		}
	}
	return false
}

// parseIntegrationMergeTree parses the complete output of one
// `merge-tree --write-tree -z --no-messages A B` invocation. The exit
// status, never the record list, derives Conflicted: exit0 writes the
// tree only, exit1 the tree plus zero or more conflict records. Another
// exit, output beyond the shared limit, incomplete NUL framing, or any
// record outside the exact grammar refuses with the zero result.
func parseIntegrationMergeTree(exitCode int, output []byte) (integrationMergeTreeResult, error) {
	if exitCode != 0 && exitCode != 1 {
		return integrationMergeTreeResult{}, integrationMergeTreeFailure(
			fmt.Sprintf("merge-tree exit status %d is outside the clean and conflicted pair the pinned command produces", exitCode),
			"capture the output of one merge-tree --write-tree -z --no-messages invocation")
	}
	if len(output) > maxGitOutput {
		return integrationMergeTreeResult{}, integrationMergeTreeFailure(
			fmt.Sprintf("merge-tree output of %d bytes exceeds the %d-byte limit", len(output), maxGitOutput),
			"measure a smaller merge or correct the captured output")
	}
	tokens, err := sliceDiffTokens(output, "merge-tree")
	if err != nil {
		return integrationMergeTreeResult{}, err
	}
	if len(tokens) == 0 {
		return integrationMergeTreeResult{}, integrationMergeTreeFailure(
			"merge-tree output carries no tree OID",
			"supply the complete merge-tree output ending in its NUL terminator")
	}
	tree := tokens[0]
	if !integrationObjectOID(tree) {
		return integrationMergeTreeResult{}, integrationMergeTreeFailure(
			fmt.Sprintf("merge-tree tree OID %q is not a full lowercase 40 or 64 digit nonzero object name", tree),
			"supply the tree OID Git wrote")
	}
	records := make([]integrationConflictStage, 0, len(tokens)-1)
	seen := make(map[string]bool, len(tokens)-1)
	for _, token := range tokens[1:] {
		record, err := parseIntegrationConflictRecord(token, len(tree))
		if err != nil {
			return integrationMergeTreeResult{}, err
		}
		key := record.Path + "\x00" + strconv.Itoa(record.Stage)
		if seen[key] {
			return integrationMergeTreeResult{}, integrationMergeTreeFailure(
				fmt.Sprintf("merge-tree records path %q at stage %d twice; duplicate conflict stages refuse", record.Path, record.Stage),
				"supply one conflict record per path and stage")
		}
		seen[key] = true
		records = append(records, record)
	}
	if exitCode == 0 && len(records) > 0 {
		return integrationMergeTreeResult{}, integrationMergeTreeFailure(
			fmt.Sprintf("clean merge-tree exit carries %d conflict records; exit0 writes the tree only", len(records)),
			"capture an output whose exit status and records belong to one invocation")
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Path != records[j].Path {
			return records[i].Path < records[j].Path
		}
		return records[i].Stage < records[j].Stage
	})
	return integrationMergeTreeResult{TreeOID: tree, Conflicted: exitCode == 1, Conflicts: records}, nil
}

const integrationConflictPathRecovery = "supply conflict paths exactly as merge-tree --write-tree -z writes them"

// integrationConflictRecordPath applies the narrower automatic-record policy:
// valid UTF-8 and POSIX containment. Glob spellings, control bytes, and tmp
// segments stay, because the bytes are Git's own conflict record.
func integrationConflictRecordPath(path string) error {
	if !utf8.ValidString(path) {
		return integrationMergeTreeFailure(fmt.Sprintf("merge-tree conflict path %q is not a valid UTF-8 path", path), integrationConflictPathRecovery)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return integrationMergeTreeFailure(fmt.Sprintf("merge-tree conflict path %q is not a normalized relative path", path), integrationConflictPathRecovery)
		}
	}
	return nil
}

// parseIntegrationConflictRecord validates one
// "%06o <oid> <stage>\t<exact-path>" record: the exact single-space field
// grammar, the closed mode and stage sets, a nonzero record OID sharing
// the tree's hash width, and the automatic-record path policy.
func parseIntegrationConflictRecord(token string, oidWidth int) (integrationConflictStage, error) {
	header, path, ok := strings.Cut(token, "\t")
	if !ok {
		return integrationConflictStage{}, integrationMergeTreeFailure(
			fmt.Sprintf("malformed merge-tree record %q: mode, OID, stage, tab, and exact path expected", token),
			"supply conflict records exactly as merge-tree --write-tree -z writes them")
	}
	fields := strings.Split(header, " ") // exact single-space grammar; whitespace variants refuse
	if len(fields) != 3 {
		return integrationConflictStage{}, integrationMergeTreeFailure(
			fmt.Sprintf("malformed merge-tree record %q: mode, OID, and stage fields expected", header),
			"supply conflict records exactly as merge-tree --write-tree -z writes them")
	}
	mode, oid := fields[0], fields[1]
	if !integrationConflictMode(mode) {
		return integrationConflictStage{}, integrationMergeTreeFailure(
			fmt.Sprintf("merge-tree conflict mode %q is outside the index-file modes the pinned command writes", mode),
			"measure conflict records of regular files, symlinks, and gitlinks only")
	}
	if !integrationObjectOID(oid) || len(oid) != oidWidth {
		return integrationConflictStage{}, integrationMergeTreeFailure(
			fmt.Sprintf("merge-tree conflict record OID %q is not a nonzero lowercase object name at the tree's %d-digit width", oid, oidWidth),
			"supply record OIDs at the tree's full object-format width")
	}
	stage, err := integrationConflictStageNumber(fields[2])
	if err != nil {
		return integrationConflictStage{}, err
	}
	if err := integrationConflictRecordPath(path); err != nil {
		return integrationConflictStage{}, err
	}
	return integrationConflictStage{Path: path, Mode: mode, OID: oid, Stage: stage}, nil
}

// integrationConflictStageNumber accepts the plain stage digits 1, 2, and
// 3 only: a signed, padded, or multi-digit spelling refuses even though a
// numeric parser would take it.
func integrationConflictStageNumber(field string) (int, error) {
	switch field {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	case "3":
		return 3, nil
	}
	return 0, integrationMergeTreeFailure(
		fmt.Sprintf("merge-tree conflict stage %q is outside the stage 1, 2, and 3 set the pinned command writes", field),
		"supply the conflict stages Git writes")
}

// integrationResolutionDiffInput carries one pinned merge-tree invocation
// beside the one resolution stream set measured against it.
type integrationResolutionDiffInput struct {
	MergeTreeExit   int
	MergeTreeOutput []byte
	Resolution      sliceDiffInput
}

// integrationResolutionDiffResult keeps the automatic tree's parse and the
// resolution measurement in separate fields, so neither attribution
// absorbs the other.
type integrationResolutionDiffResult struct {
	AutomaticTree integrationMergeTreeResult
	Resolution    sliceDiffResult
}

// measureIntegrationResolutionDiff first parses the automatic tree, then
// measures the supplied resolution edits exactly once through the
// slice-diff owner, unchanged. A clean exit0 tree still charges every
// supplied resolution edit, and the automatic delta is never measured as
// authorship. Any refusal returns the zero result.
func measureIntegrationResolutionDiff(in integrationResolutionDiffInput) (integrationResolutionDiffResult, error) {
	tree, err := parseIntegrationMergeTree(in.MergeTreeExit, in.MergeTreeOutput)
	if err != nil {
		return integrationResolutionDiffResult{}, err
	}
	resolution, err := measureSliceDiff(in.Resolution)
	if err != nil {
		return integrationResolutionDiffResult{}, err
	}
	return integrationResolutionDiffResult{AutomaticTree: tree, Resolution: resolution}, nil
}
