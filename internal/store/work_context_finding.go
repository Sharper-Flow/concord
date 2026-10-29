package store

// WorkContextFinding is a declared claim. It discharges no predicate and
// records no verdict. Domain ownership is checked by ValidateWorkContextDomainTx.
type WorkContextFinding struct {
	Kind string `json:"kind"`
	Statement string `json:"statement"`
	SubjectRef string `json:"subject_ref"`
	EvidenceRefs []string `json:"evidence_refs"`
	DomainID string `json:"domain_id"`
	ProductWideRationale string `json:"product_wide_rationale,omitempty"`
}

func validateWorkContextFindingEntry(finding WorkContextFinding) error {
	bad := func(detail string) error {
		return workContextFailure(KindInvalidPayload, "work_context", detail, "supply a bounded context finding")
	}
	switch finding.Kind {
	case "observation", "inference", "hypothesis", "rejected_approach", "open_question", "contradiction", "direction":
	default:
		return bad("context_finding kind must be observation, inference, hypothesis, rejected_approach, open_question, contradiction, or direction")
	}
	if len(finding.Statement)<1 || len(finding.Statement)>1024 { return bad("context_finding statement must be between 1 and 1024 UTF-8 bytes") }
	if len(finding.SubjectRef)<1 || len(finding.SubjectRef)>128 { return bad("context_finding subject_ref must be between 1 and 128 UTF-8 bytes") }
	if finding.EvidenceRefs==nil || len(finding.EvidenceRefs)>8 { return bad("context_finding requires an evidence_refs array of at most 8 entries") }
	for _,ref:=range finding.EvidenceRefs {
		if len(ref)<1 || len(ref)>256 { return bad("context_finding evidence ref must be between 1 and 256 UTF-8 bytes") }
	}
	if len(finding.DomainID)<1 || len(finding.DomainID)>256 { return bad("context_finding domain_id must be between 1 and 256 UTF-8 bytes") }
	if len(finding.ProductWideRationale)>512 { return bad("context_finding product_wide_rationale must be at most 512 UTF-8 bytes") }
	return nil
}
