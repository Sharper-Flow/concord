package store

import "encoding/json"

// WorkflowRetryConvergence validates persisted dispatch completion metadata.
// Current dispatch admission does not derive or consume these facts.
type WorkflowRetryConvergence struct {
	Basis             string `json:"basis"`
	PreviousRecordSeq int64  `json:"previous_record_seq,omitempty"`
	LatestRecordSeq   int64  `json:"latest_record_seq,omitempty"`
	SupersededSeq     int64  `json:"superseded_seq,omitempty"`
	HostPID           int    `json:"host_pid,omitempty"`
	HostPIDStart      uint64 `json:"host_pid_start,omitempty"`
}

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

func validateWorkflowOpenFindingsPayload(fields map[string]json.RawMessage) error {
	raw, present := fields["open_finding_ids"]
	if !present {
		return nil
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || !validWorkflowOpenFindings(ids) {
		return newFailure(KindInvalidPayload, "workflow_action", "open_finding_ids must contain 1 to 32 unique finding IDs", false, "supply the complete open finding set with stable IDs")
	}
	return nil
}

func (c WorkflowRetryConvergence) valid() bool {
	switch c.Basis {
	case "findings_shrinking":
		return c.PreviousRecordSeq > 0 && c.LatestRecordSeq > c.PreviousRecordSeq && c.SupersededSeq == 0 && c.HostPID == 0 && c.HostPIDStart == 0
	case "approach_changed":
		return c.SupersededSeq > 0 && c.PreviousRecordSeq == 0 && c.LatestRecordSeq == 0 && c.HostPID == 0 && c.HostPIDStart == 0
	case "host_changed":
		return c.LatestRecordSeq > 0 && c.PreviousRecordSeq == 0 && c.SupersededSeq == 0 && c.HostPID > 0 && c.HostPIDStart > 0
	}
	return false
}
