package store

// Pure committed-diff measurement for one slice: the same Git text input
// always yields the same sorted result or the same typed refusal. No Git
// process, store, caller path, or clock enters the measurement.

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func sliceDiffFailure(detail, recovery string) error {
	return newFailure(KindInvalidPayload, "worker_slice_diff", detail, false, recovery)
}

const (
	sliceDiffModeNull       = "000000"
	sliceDiffModeRegular    = "100644"
	sliceDiffModeExecutable = "100755"
)

// sliceDiffInput carries one complete measurement: exact Git text, blob bytes, and the architecture vocabulary.
type sliceDiffInput struct {
	ProjectID  string
	RawDiff    []byte
	Numstat    []byte
	OldBlobs   map[string][]byte
	NewBlobs   map[string][]byte
	Files      []WorkflowDesignArchitectureFile
	SliceFiles []string
	Unchanged  []string
	Limit      int
}

// slicePathCounts reports one changed path; sliceDiffResult the sorted list plus the source-role total.
type slicePathCounts struct {
	Path      string
	Role      string
	Additions int
	Deletions int
}

type sliceDiffResult struct {
	Paths      []slicePathCounts
	TotalLines int
}

type sliceNumstatRecord struct {
	additions int
	deletions int
	binary    bool
	path      string
}

func sliceDiffModeIsRegular(mode string) bool {
	return mode == sliceDiffModeRegular || mode == sliceDiffModeExecutable
}

func sliceDiffOIDHex(oid string) bool {
	return oid != "" && strings.IndexFunc(oid, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
	}) < 0
}

func measureSliceDiff(in sliceDiffInput) (sliceDiffResult, error) {
	if in.Limit < designArchitectureMinChangedLines || in.Limit > designArchitectureMaxChangedLines {
		return sliceDiffResult{}, sliceDiffFailure(fmt.Sprintf("declared slice limit %d is outside %d..%d", in.Limit, designArchitectureMinChangedLines, designArchitectureMaxChangedLines), "declare the slice limit within its closed bounds")
	}
	paths, statusByPath, err := parseSliceRawDiff(in.RawDiff)
	if err != nil {
		return sliceDiffResult{}, err
	}
	records, err := parseSliceNumstat(in.Numstat)
	if err != nil {
		return sliceDiffResult{}, err
	}
	numstatByPath := make(map[string]sliceNumstatRecord, len(records))
	for _, record := range records {
		numstatByPath[record.path] = record
	}
	for _, path := range paths {
		if _, ok := numstatByPath[path]; !ok {
			return sliceDiffResult{}, sliceDiffFailure(fmt.Sprintf("numstat records do not cover changed path %q", path), "measure the same base and end with both pinned Git options")
		}
	}
	if len(numstatByPath) != len(paths) {
		for _, record := range records {
			if _, ok := statusByPath[record.path]; !ok {
				return sliceDiffResult{}, sliceDiffFailure(fmt.Sprintf("numstat records path %q that the raw diff does not change", record.path), "measure the same base and end with both pinned Git options")
			}
		}
	}
	roleByPath, err := indexSliceDiffRoles(in)
	if err != nil {
		return sliceDiffResult{}, err
	}
	sliceFiles, unchanged := make(map[string]bool, len(in.SliceFiles)), make(map[string]bool, len(in.Unchanged))
	for _, path := range in.SliceFiles {
		sliceFiles[path] = true
	}
	for _, path := range in.Unchanged {
		unchanged[path] = true
	}
	result := sliceDiffResult{Paths: make([]slicePathCounts, 0, len(paths))}
	for _, path := range paths {
		counts, err := measureSliceDiffPath(in, statusByPath[path], numstatByPath[path], roleByPath, sliceFiles, unchanged, path)
		if err != nil {
			return sliceDiffResult{}, err
		}
		if counts.Role == DesignArchitectureFileRoleSource {
			cost := counts.Additions + counts.Deletions
			if cost > math.MaxInt-result.TotalLines {
				return sliceDiffResult{}, sliceDiffFailure(fmt.Sprintf("counted total overflows the integer range at path %q", path), "measure a smaller slice")
			}
			result.TotalLines += cost
		}
		result.Paths = append(result.Paths, counts)
	}
	if result.TotalLines > in.Limit {
		return sliceDiffResult{}, sliceDiffFailure(fmt.Sprintf("slice measures %d changed lines above its declared limit %d", result.TotalLines, in.Limit), "declare a limit that covers the measured cost or reduce the diff")
	}
	return result, nil
}

// indexSliceDiffRoles indexes the approved file map for the measured
// Project; roles come from this map alone and duplicate entries refuse.
func indexSliceDiffRoles(in sliceDiffInput) (map[string]string, error) {
	roleByPath := make(map[string]string)
	for _, file := range in.Files {
		if file.ProjectID != in.ProjectID {
			continue
		}
		if _, ok := roleByPath[file.Path]; ok {
			return nil, sliceDiffFailure(fmt.Sprintf("approved file map declares %q twice; duplicate role entries refuse instead of reclassifying", file.Path), "declare each file-map entry once")
		}
		if file.Role != DesignArchitectureFileRoleSource && file.Role != DesignArchitectureFileRoleTest && file.Role != DesignArchitectureFileRoleGenerated {
			return nil, sliceDiffFailure(fmt.Sprintf("approved role %q on %q is not a declared file role", file.Role, file.Path), "declare source, test, or generated roles only")
		}
		roleByPath[file.Path] = file.Role
	}
	return roleByPath, nil
}

func measureSliceDiffPath(in sliceDiffInput, status string, record sliceNumstatRecord, roleByPath map[string]string, sliceFiles, unchanged map[string]bool, path string) (slicePathCounts, error) {
	if !sliceFiles[path] {
		return slicePathCounts{}, sliceDiffFailure(fmt.Sprintf("changed path %q is outside the slice's declared files", path), "measure the slice whose declared files cover every changed path")
	}
	if unchanged[path] {
		return slicePathCounts{}, sliceDiffFailure(fmt.Sprintf("changed path %q is declared unchanged but the diff carries it", path), "remove the path from unchanged_files or correct the measured range")
	}
	role, ok := roleByPath[path]
	if !ok {
		return slicePathCounts{}, sliceDiffFailure(fmt.Sprintf("changed path %q carries no approved file-map role for project %q", path, in.ProjectID), "add the path to the approved file map with its role")
	}
	counts := slicePathCounts{Path: path, Role: role, Additions: record.additions, Deletions: record.deletions}
	if record.binary {
		if role == DesignArchitectureFileRoleSource {
			return slicePathCounts{}, sliceDiffFailure(fmt.Sprintf("counted source path %q shows a binary numstat pair; binary source content cannot count", path), "measure text numstat for source paths or correct the approved role")
		}
		counts.Additions, counts.Deletions = 0, 0
	}
	if role == DesignArchitectureFileRoleSource {
		if err := checkSliceDiffBlob(in.OldBlobs, path, "old", status != "A"); err != nil {
			return slicePathCounts{}, err
		}
		if err := checkSliceDiffBlob(in.NewBlobs, path, "new", status != "D"); err != nil {
			return slicePathCounts{}, err
		}
	}
	return counts, nil
}

// checkSliceDiffBlob requires a counted source path's blob bytes to be
// present, valid UTF-8, and free of NUL; blob bytes carry shape, not counts.
func checkSliceDiffBlob(blobs map[string][]byte, path, side string, required bool) error {
	if !required {
		return nil
	}
	blob, ok := blobs[path]
	if !ok {
		return sliceDiffFailure(fmt.Sprintf("counted source path %q is missing its %s blob bytes", path, side), "supply the blob bytes for every counted source path")
	}
	if !utf8.Valid(blob) || bytes.IndexByte(blob, 0) >= 0 {
		return sliceDiffFailure(fmt.Sprintf("counted source blob for %q is not valid UTF-8 text without a NUL byte", path), "measure only text source paths or correct the approved role")
	}
	return nil
}

// sliceDiffTokens splits one NUL-framed Git stream, refusing a missing terminator or empty record.
func sliceDiffTokens(stream []byte, label string) ([]string, error) {
	if len(stream) == 0 {
		return nil, nil
	}
	if stream[len(stream)-1] != 0 {
		return nil, sliceDiffFailure(fmt.Sprintf("malformed %s stream: it does not end with its NUL terminator", label), "supply the complete Git output")
	}
	tokens := strings.Split(string(stream[:len(stream)-1]), "\x00")
	for _, token := range tokens {
		if token == "" {
			return nil, sliceDiffFailure(fmt.Sprintf("malformed %s stream: it carries an empty record", label), "supply complete Git output without interior NUL records")
		}
	}
	return tokens, nil
}

// parseSliceRawDiff returns the changed paths sorted with their statuses.
func parseSliceRawDiff(raw []byte) ([]string, map[string]string, error) {
	tokens, err := sliceDiffTokens(raw, "raw diff")
	if err != nil {
		return nil, nil, err
	}
	if len(tokens)%2 != 0 {
		return nil, nil, sliceDiffFailure("malformed raw diff stream: it ends with a header and no path record", "supply complete raw diff records")
	}
	paths := make([]string, 0, len(tokens)/2)
	statusByPath := make(map[string]string, len(tokens)/2)
	for i := 0; i < len(tokens); i += 2 {
		status, err := parseSliceRawHeader(tokens[i], tokens[i+1])
		if err != nil {
			return nil, nil, err
		}
		if _, ok := statusByPath[tokens[i+1]]; ok {
			return nil, nil, sliceDiffFailure(fmt.Sprintf("raw diff records path %q twice; duplicate changed paths refuse", tokens[i+1]), "supply one raw record per changed path")
		}
		statusByPath[tokens[i+1]] = status
		paths = append(paths, tokens[i+1])
	}
	sort.Strings(paths)
	return paths, statusByPath, nil
}

// parseSliceRawHeader validates one `--raw -z --no-renames` record and
// returns its status; modes pair with status, objects are hex, paths exact.
func parseSliceRawHeader(header, path string) (string, error) {
	if !strings.HasPrefix(header, ":") {
		return "", sliceDiffFailure(fmt.Sprintf("malformed raw diff header %q: the leading colon is missing", header), "supply raw diff headers exactly as --raw -z writes them")
	}
	fields := strings.Split(header, " ") // exact single-space grammar; whitespace variants refuse
	if len(fields) != 5 {
		return "", sliceDiffFailure(fmt.Sprintf("malformed raw diff header %q: mode, mode, object, object, and status expected", header), "supply raw diff headers exactly as --raw -z writes them")
	}
	for _, oid := range []string{fields[2], fields[3]} {
		if !sliceDiffOIDHex(oid) {
			return "", sliceDiffFailure(fmt.Sprintf("malformed raw diff header: object %q is not hexadecimal", oid), "supply the object names Git writes")
		}
	}
	status := fields[4]
	srcMode := strings.TrimPrefix(fields[0], ":")
	dstMode := fields[1]
	if status != "A" && status != "D" && status != "M" {
		return "", sliceDiffFailure(fmt.Sprintf("raw diff status %q is outside the addition, deletion, modification set the pinned options produce", status), "run the measurement with --no-renames so a rename arrives as deletion plus addition")
	}
	pairOK := status == "A" && srcMode == sliceDiffModeNull && sliceDiffModeIsRegular(dstMode) ||
		status == "D" && sliceDiffModeIsRegular(srcMode) && dstMode == sliceDiffModeNull ||
		status == "M" && sliceDiffModeIsRegular(srcMode) && sliceDiffModeIsRegular(dstMode)
	if !pairOK {
		return "", sliceDiffFailure(fmt.Sprintf("raw diff %s record carries modes %s to %s; only regular files and the null mode are measured", status, srcMode, dstMode), "measure regular file changes only; symlinks, gitlinks, and other modes refuse")
	}
	if err := sliceDiffVocabulary(path); err != nil {
		return "", err
	}
	return status, nil
}

// sliceDiffVocabulary applies the shared path policy in its accepted order
// and names the exact path bytes in every refusal, so a split path cannot hide.
func sliceDiffVocabulary(path string) error {
	if err := validateWorkContextRepoPath(path); err != nil {
		return sliceDiffFailure(fmt.Sprintf("changed path %q refused: %v", path, err), "supply the exact repository path bytes without splitting or normalization")
	}
	if err := designArchitectureContainedPath("changed", path); err != nil {
		return sliceDiffFailure(fmt.Sprintf("changed path %q refused: %v", path, err), "supply an exact contained path in valid UTF-8 without glob spellings")
	}
	return nil
}

// parseSliceNumstat splits on the first two tabs only, so a path keeps its
// exact bytes and an unsafe spelling refuses through the shared vocabulary.
func parseSliceNumstat(numstat []byte) ([]sliceNumstatRecord, error) {
	tokens, err := sliceDiffTokens(numstat, "numstat")
	if err != nil {
		return nil, err
	}
	records := make([]sliceNumstatRecord, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		parts := strings.SplitN(token, "\t", 3)
		if len(parts) != 3 {
			return nil, sliceDiffFailure(fmt.Sprintf("malformed numstat record %q: additions, deletions, and path expected", token), "supply numstat records exactly as --numstat -z writes them")
		}
		path := parts[2]
		if seen[path] {
			return nil, sliceDiffFailure(fmt.Sprintf("numstat records path %q twice; duplicate numstat records refuse", path), "supply one numstat record per changed path")
		}
		seen[path] = true
		if err := sliceDiffVocabulary(path); err != nil {
			return nil, err
		}
		additions, deletions, err := parseSliceNumstatPair(parts[0], parts[1], path)
		if err != nil {
			return nil, err
		}
		records = append(records, sliceNumstatRecord{additions: additions, deletions: deletions, binary: parts[0] == "-", path: path})
	}
	return records, nil
}

func parseSliceNumstatPair(additions, deletions, path string) (int, int, error) {
	if additions == "-" || deletions == "-" {
		if additions != "-" || deletions != "-" {
			return 0, 0, sliceDiffFailure(fmt.Sprintf("malformed numstat record for %q: the binary marker covers both fields", path), "supply the numstat pair Git writes")
		}
		return 0, 0, nil
	}
	added, err := parseSliceNumstatCount(additions, path)
	if err != nil {
		return 0, 0, err
	}
	deleted, err := parseSliceNumstatCount(deletions, path)
	if err != nil {
		return 0, 0, err
	}
	if deleted > math.MaxInt-added {
		return 0, 0, sliceDiffFailure(fmt.Sprintf("numstat pair %d plus %d for %q overflows the integer range", added, deleted, path), "supply the counts Git wrote")
	}
	return added, deleted, nil
}

// parseSliceNumstatCount accepts a plain digit run only: signs, decimals,
// and other spellings refuse even though a signed parser would take them.
func parseSliceNumstatCount(field, path string) (int, error) {
	if field == "" {
		return 0, sliceDiffFailure(fmt.Sprintf("malformed numstat record for %q: a count field is empty", path), "supply the counts Git wrote")
	}
	for _, r := range field {
		if r < '0' || r > '9' {
			return 0, sliceDiffFailure(fmt.Sprintf("malformed numstat record for %q: count field %q is not a plain digit run", path, field), "supply the decimal counts Git wrote")
		}
	}
	value, err := strconv.ParseInt(field, 10, 0)
	if err != nil {
		return 0, sliceDiffFailure(fmt.Sprintf("numstat count %q for %q overflows the integer range", field, path), "supply the counts Git wrote")
	}
	return int(value), nil
}
