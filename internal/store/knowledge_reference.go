package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

// parseQualifiedKnowledgeID splits the source-qualified reference form
// "project_id/law_id" (CD-0200). Project IDs cannot contain '/', and the law
// folder refuses '/' inside a stable law ID, so the first '/' is the exact
// split point and a second '/' is a malformed reference, never a nested path.
// A bare reference is its own law ID.
func parseQualifiedKnowledgeID(op, value string) (projectID, lawID string, qualified bool, err error) {
	projectID, lawID, qualified = strings.Cut(value, "/")
	if !qualified {
		return "", value, false, nil
	}
	if projectID == "" || lawID == "" || strings.Contains(lawID, "/") {
		return "", "", true, newFailure(KindInvalidFilter, op, "qualified knowledge reference must be project_id/law_id", false, "supply a Project identifier and a law ID separated by one '/'")
	}
	return projectID, lawID, true, nil
}

// uniqueSorted drops empty and repeated values from a list and sorts the
// result, the canonical form relation and applicability lists answer in.
func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// decodeCursorFields decodes exactly one JSON object and refuses a key the
// current cursor shape does not declare. A cursor minted under a retired
// filter names that filter, so it cannot resume a query that no longer
// applies it.
func decodeCursorFields(b []byte, cursor any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cursor); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("cursor carries trailing data")
	}
	return nil
}
