package store

import (
	"fmt"
	"regexp"
	"strings"
)

// fenceLine matches the fenced-code delimiter scripts/check-durable-tier.py
// recognises. The pattern is copied from that script deliberately: the producer
// and the detective layer must agree on what a fence is, or a note the CI
// validator would reject could still be published.
var fenceLine = regexp.MustCompile("^```[ \t]*([A-Za-z0-9_-]*)[ \t]*$")

// oversizeFencedJSON returns the byte length of the first fenced JSON block
// exceeding the bound, or zero when every block is within it.
//
// The scan mirrors fenced_json_blocks in scripts/check-durable-tier.py: a block
// opens on a fence whose info string names json, closes on a bare fence, and is
// measured over its inner lines joined by newlines. A fence naming any other
// language opens nothing, so its contents are not measured.
func oversizeFencedJSON(content string, bound int) int {
	var current []string
	open := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if match := fenceLine.FindStringSubmatch(line); match != nil && !open {
			if strings.Contains(strings.ToLower(match[1]), "json") {
				open = true
				current = nil
			}
			continue
		}
		if open && strings.TrimSpace(line) == "```" {
			if size := len(strings.Join(current, "\n")); size > bound {
				return size
			}
			open = false
			current = nil
			continue
		}
		if open {
			current = append(current, line)
		}
	}
	// An unterminated block is still content the note carries.
	if open {
		if size := len(strings.Join(current, "\n")); size > bound {
			return size
		}
	}
	return 0
}

// CheckDurableTier rejects a note the durable-tier budget does not permit.
//
// CD-0069 D3 puts this at the producer so a compliant path can never emit a
// violation. The CI validator remains the detective layer over what is already
// committed; it cannot stop a write, and by the time it runs the note is in
// history. Both read the same bounds, one generated and one loaded, from
// docs/durable-tier-budget.v1.json.
//
// It is exported because the AJ6 corpus binding asserts the emitted note
// against the same rule the producer applied. A second copy of the rule in the
// corpus would agree with the producer until one of them changed.
func CheckDurableTier(content string) error {
	if size := len(content); size > maxDurableNoteBytes {
		return newFailure(KindInvalidNoteProof, "publish_note", fmt.Sprintf("note is %d bytes and the durable tier bound is %d", size, maxDurableNoteBytes), false, "distill the note to a page rather than a state dump")
	}
	if size := oversizeFencedJSON(content, maxDurableFencedJSONBytes); size > 0 {
		return newFailure(KindInvalidNoteProof, "publish_note", fmt.Sprintf("note carries a fenced JSON block of %d bytes and the durable tier bound is %d", size, maxDurableFencedJSONBytes), false, "distill the block rather than embedding state in the note")
	}
	return nil
}
