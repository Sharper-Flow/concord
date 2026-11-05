package store

import (
	"sort"
	"strings"
)

func orderedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

// parseQualifiedKnowledgeID splits the source-qualified reference form
// "project_id/law_id". Project IDs cannot contain '/', and the law
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
