package store

import "fmt"

const WorkContextSourceKindReviewFinding = "review_finding"

func rankedFindingView(finding ReviewFinding, kind, eventID string, eventSeq int64, ordinal int, state *WorkflowReviewState) WorkContextFindingView {
	view := WorkContextFindingView{
		FindingID: fmt.Sprintf("finding:%d:%d", eventSeq, ordinal),
		Statement: finding.Detail, SubjectRef: "review-finding", DomainID: "unresolved",
		EvidenceRefs: finding.EvidenceRefs, Origin: workContextOriginForKind(kind),
		Status: WorkContextFindingStatusReported, SourceEventID: eventID,
		SourceEventSeq: eventSeq, Ordinal: ordinal, SourceKind: WorkContextSourceKindReviewFinding,
		Oracle: finding.Oracle,
	}
	if finding.Oracle != nil && finding.Oracle.OwnerID != "" {
		view.SubjectRef = finding.Oracle.OwnerID
	}
	if state != nil {
		if source, retained := state.findingSources[finding.FindingID]; retained && source.DomainID != "" {
			view.DomainID = source.DomainID
		}
		for _, current := range state.Findings {
			if current.FindingID == finding.FindingID && current.Status == "open" && current.InScope && current.Blocking {
				view.Status = WorkContextFindingStatusOpen
			}
		}
	}
	return view
}

// Context selections cannot omit an open blocker retained by the Review owner.
func projectOpenReviewContextFindings(state WorkflowReviewState, add func(WorkContextFindingView)) error {
	for _, finding := range state.Findings {
		if finding.Status != "open" || !finding.InScope || !finding.Blocking {
			continue
		}
		source, retained := state.findingSources[finding.FindingID]
		if !retained || source.EventID == "" || source.Seq < 1 {
			return workContextFailure(KindInvariantViolation, "work_context_read", "open Review finding "+finding.FindingID+" has no retained source identity", "restore the verified Review finding source")
		}
		view := rankedFindingView(finding, source.Kind, source.EventID, source.Seq, source.Ordinal, &state)
		view.FindingID = source.CanonicalID
		add(view)
	}
	return nil
}
