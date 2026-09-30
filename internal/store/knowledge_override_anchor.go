package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"strconv"
	"strings"
)

// CD-0194 D2 makes an explicit operator override the only route that admits a
// Product knowledge location outside the layout tier's prefix. The manifest
// head's override names an anchor record, and this file owns the proof that
// the anchor is real: an accepted decision whose immutable document blob
// carries the closed concord-operator-override instruction block for the
// override's exact Product and path with the approve decision. Composition
// proves shape; this gate proves the anchor on every committed manifest read,
// so a fabricated, unrelated, superseded, or denied anchor refuses before the
// manifest is used.

// operatorInstructionOpen is the exact line that opens one closed
// instruction block. The block is an HTML comment, which CommonMark keeps
// out of the rendered document, so the grammar never competes with prose.
const operatorInstructionOpen = "<!-- concord-operator-override"

// operatorInstructionClose is the exact line that closes one block.
const operatorInstructionClose = "-->"

// overrideInstruction is one parsed instruction block. Product and Path are
// the pair the block names; decision is approve or deny.
type overrideInstruction struct {
	product  string
	path     string
	decision string
}

// parseOverrideInstructionBlocks extracts the closed operator-instruction
// blocks from a decision document. A block opens with the opener line, closes
// with a lone close line, and carries exactly the fields product, path, and
// decision; decision is approve or deny. Anything else inside a block is a
// malformation, and prose outside blocks is never read for an instruction.
// Fenced code blocks are skipped: an instruction shown inside a fenced
// example documents the grammar, it never grants a placement.
func parseOverrideInstructionBlocks(content []byte) (instructions []overrideInstruction, malformations []string) {
	fail := func(line int, detail string) {
		malformations = append(malformations, "line "+strconv.Itoa(line)+": "+detail)
	}
	closeBlock := func(fields map[string]string, seen map[string]bool, startLine int) {
		for _, required := range []string{"product", "path", "decision"} {
			if !seen[required] {
				fail(startLine, "instruction block is missing the "+required+" field")
			}
		}
		if decision := fields["decision"]; decision != "" && decision != "approve" && decision != "deny" {
			fail(startLine, "instruction decision must be 'approve' or 'deny', not "+decision)
		}
		instructions = append(instructions, overrideInstruction{
			product: fields["product"], path: fields["path"], decision: fields["decision"],
		})
	}
	inFence, fenceChar := false, byte(0)
	inBlock, blockLine := false, 0
	fields := map[string]string{}
	seen := map[string]bool{}
	for number, raw := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		stripped := strings.TrimSpace(raw)
		if inFence {
			if fenceRun(stripped, fenceChar) {
				inFence = false
			}
			continue
		}
		if fenceOpener(stripped) {
			inFence = true
			fenceChar = stripped[0]
			continue
		}
		if !inBlock {
			if stripped == operatorInstructionOpen {
				inBlock = true
				blockLine = number + 1
				fields = map[string]string{}
				seen = map[string]bool{}
			}
			continue
		}
		if stripped == operatorInstructionClose {
			inBlock = false
			closeBlock(fields, seen, blockLine)
			continue
		}
		if strings.Contains(stripped, operatorInstructionClose) {
			fail(number+1, "instruction block must close on its own line with '-->'")
			continue
		}
		name, rest, found := strings.Cut(stripped, ":")
		value := strings.TrimSpace(rest)
		switch {
		case !found:
			fail(number+1, "instruction line must be a 'name: value' field: "+stripped)
		case name != "product" && name != "path" && name != "decision":
			fail(number+1, "instruction field must be one of decision, path, product: "+stripped)
		case value == "":
			fail(number+1, "instruction field "+name+" carries no value")
		case seen[name]:
			fail(number+1, "duplicate instruction field "+name)
		default:
			seen[name] = true
			fields[name] = value
		}
	}
	if inBlock {
		fail(blockLine, "instruction block is never closed by '-->'")
	}
	return instructions, malformations
}

// fenceOpener reports whether the stripped line opens a fenced code block:
// three or more backticks or tildes, optionally followed by an info string.
func fenceOpener(stripped string) bool {
	if len(stripped) < 3 {
		return false
	}
	char := stripped[0]
	return (char == '`' || char == '~') && stripped[1] == char && stripped[2] == char
}

// fenceRun reports whether the stripped line closes the open fence: the same
// character as the opener, at least three of them, and nothing else.
func fenceRun(stripped string, char byte) bool {
	if len(stripped) < 3 {
		return false
	}
	for index := 0; index < len(stripped); index++ {
		if stripped[index] != char {
			return false
		}
	}
	return true
}

// validateOverrideAnchors proves every operator override in the manifest
// resolves to a real accepted decision whose immutable blob carries the
// closed approve instruction for the override's exact Product and path. read
// supplies one document's bytes by repository-relative path, so the same gate
// serves a committed read and a working-tree read. A manifest whose head
// carries no overrides admits everything and proves nothing here.
func validateOverrideAnchors(manifest KnowledgeManifest, read func(string) ([]byte, error)) error {
	if len(manifest.OperatorOverrides) == 0 {
		return nil
	}
	byID := make(map[string]KnowledgeRecord, len(manifest.Records))
	for _, record := range manifest.Records {
		byID[record.ID] = record
	}
	for _, override := range manifest.OperatorOverrides {
		if err := validateOneOverrideAnchor(override, byID, read); err != nil {
			return err
		}
	}
	return nil
}

func validateOneOverrideAnchor(override KnowledgeOperatorOverride, byID map[string]KnowledgeRecord, read func(string) ([]byte, error)) error {
	anchor, ok := byID[override.RecordedIn]
	if !ok {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "operator override recorded_in names no manifest record: "+override.RecordedIn, false, "record the operator's instruction in an accepted decision the manifest carries")
	}
	if anchor.Kind != "decision" {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "operator override anchor "+anchor.ID+" is a "+anchor.Kind+" record; only a decision carries operator override authority", false, "record the instruction in an accepted decision")
	}
	if anchor.Status != "accepted" {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "operator override anchor "+anchor.ID+" has status "+anchor.Status+"; only an accepted decision carries operator override authority", false, "record the instruction in an accepted decision")
	}
	content, err := read(anchor.Path)
	if err != nil {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor document "+anchor.Path+" cannot be read, so it carries no operator instruction", false, "restore the anchor decision document")
	}
	sum := sha256.Sum256(content)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != anchor.SHA256 {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor document "+anchor.Path+" does not match the record's immutable hash proof", false, "recompute the record's sha256 over the committed document")
	}
	instructions, malformations := parseOverrideInstructionBlocks(content)
	for _, malformation := range malformations {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor instruction is malformed: "+malformation, false, "repair the closed concord-operator-override block in "+anchor.Path)
	}
	var matching []overrideInstruction
	for _, instruction := range instructions {
		if instruction.product == override.ProductID && instruction.path == override.Path {
			matching = append(matching, instruction)
		}
	}
	if len(matching) > 1 {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor document carries more than one operator instruction for Product "+override.ProductID+" at "+override.Path, false, "record one instruction per placement")
	}
	if len(matching) == 0 {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor document carries no operator instruction for Product "+override.ProductID+" at "+override.Path, false, "add the closed concord-operator-override block naming both to "+anchor.Path)
	}
	if matching[0].decision == "deny" {
		return newFailure(KindInvalidNoteProof, "resolve_override_anchor", "override anchor document records a denial, not an approval, for Product "+override.ProductID+" at "+override.Path, false, "obtain the operator's approval before indexing the placement")
	}
	return nil
}

// committedOverrideAnchorReader reads one record document's bytes from the
// blob a commit carries, so the anchor gate proves the committed instruction,
// never a working-tree copy.
func committedOverrideAnchorReader(ctx context.Context, repo, commit string) func(string) ([]byte, error) {
	return func(relPath string) ([]byte, error) {
		out, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+relPath)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

// workingTreeOverrideAnchorReader reads one record document's bytes from the
// checked-out repository, the form lesson publication composes before it
// commits.
func workingTreeOverrideAnchorReader(repo string) func(string) ([]byte, error) {
	return func(relPath string) ([]byte, error) {
		return os.ReadFile(path.Join(repo, relPath)) //nolint:gosec // relPath is a validated manifest record path and repo is the operator-selected Git authority.
	}
}
