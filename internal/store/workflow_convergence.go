package store

func validWorkflowOpenFindings(ids []string) bool {
	if len(ids) < 1 || len(ids) > 32 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] || !workflowCorrectionID(id) {
			return false
		}
		seen[id] = true
	}
	return true
}
