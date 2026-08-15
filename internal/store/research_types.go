package store

import "encoding/json"

type ResearchFreshness string

const (
	ResearchCurrent ResearchFreshness = "current"
	ResearchStale   ResearchFreshness = "stale"
	ResearchUnknown ResearchFreshness = "unknown"
)

type ResearchFindingKind string
type ResearchConfidence string
type ResearchFindingStatus string
type ResearchSourceKind string
type ResearchUseRole string

const (
	FindingObservation    ResearchFindingKind   = "observation"
	FindingInference      ResearchFindingKind   = "inference"
	FindingHypothesis     ResearchFindingKind   = "hypothesis"
	FindingConclusion     ResearchFindingKind   = "conclusion"
	FindingRecommendation ResearchFindingKind   = "recommendation"
	ConfidenceLow         ResearchConfidence    = "low"
	ConfidenceMedium      ResearchConfidence    = "medium"
	ConfidenceHigh        ResearchConfidence    = "high"
	FindingActive         ResearchFindingStatus = "active"
	FindingContradicted   ResearchFindingStatus = "contradicted"
	FindingSuperseded     ResearchFindingStatus = "superseded"
	SourceOfficialDoc     ResearchSourceKind    = "official_doc"
	SourceCode            ResearchSourceKind    = "source_code"
	SourceIssue           ResearchSourceKind    = "issue"
	SourcePaper           ResearchSourceKind    = "paper"
	SourceWeb             ResearchSourceKind    = "web"
	SourceLocalEvidence   ResearchSourceKind    = "local_evidence"
	UseContext            ResearchUseRole       = "context"
	UseDesignInput        ResearchUseRole       = "design_input"
	UseVerificationBasis  ResearchUseRole       = "verification_basis"
	UseDecisionBasis      ResearchUseRole       = "decision_basis"
)

type ResearchMutationIdentity struct {
	PrincipalRef   string `json:"principal_ref"`
	Tool           string `json:"tool"`
	OperationKind  string `json:"operation_kind"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ResearchRevisionInput struct {
	Question string          `json:"question"`
	ScopeIn  json.RawMessage `json:"scope_in"`
	ScopeOut json.RawMessage `json:"scope_out"`
	DoneWhen json.RawMessage `json:"done_when"`
	Method   string          `json:"method"`
}

type ResearchFinding struct {
	PackID     string                `json:"pack_id"`
	Revision   int64                 `json:"revision"`
	FindingID  string                `json:"finding_id"`
	Kind       ResearchFindingKind   `json:"kind"`
	Statement  string                `json:"statement"`
	Confidence ResearchConfidence    `json:"confidence"`
	Freshness  ResearchFreshness     `json:"freshness"`
	Status     ResearchFindingStatus `json:"status"`
	SourceIDs  []string              `json:"source_ids,omitempty"`
	Scopes     ResearchScopes        `json:"scopes"`
}

// ResearchScopes declares what a finding applies to, using the same vocabulary as
// durable knowledge records so one scope shape spans active and durable knowledge.
// Mode home means the finding applies to its owner's home broadly and carries no
// explicit IDs; explicit means it applies to exactly the declared scopes.
type ResearchScopes struct {
	Mode         string   `json:"mode"`
	ProductIDs   []string `json:"product_ids,omitempty"`
	ProjectIDs   []string `json:"project_ids,omitempty"`
	ComponentIDs []string `json:"component_ids,omitempty"`
	TagIDs       []string `json:"tag_ids,omitempty"`
}

// byKind pairs each scope list with its stored scope_kind, so writers and readers
// share one ordering and cannot disagree about which list a kind belongs to.
func (s *ResearchScopes) byKind() []struct {
	kind   string
	values *[]string
} {
	return []struct {
		kind   string
		values *[]string
	}{
		{"product", &s.ProductIDs},
		{"project", &s.ProjectIDs},
		{"component", &s.ComponentIDs},
		{"tag", &s.TagIDs},
	}
}

type ResearchSource struct {
	PackID            string             `json:"pack_id"`
	Revision          int64              `json:"revision"`
	SourceID          string             `json:"source_id"`
	Kind              ResearchSourceKind `json:"kind"`
	Locator           string             `json:"locator"`
	Title             string             `json:"title"`
	PublisherOrAuthor string             `json:"publisher_or_author"`
	PublishedAt       string             `json:"published_at,omitempty"`
	AccessedAt        string             `json:"accessed_at"`
}

type ResearchConsumer struct {
	PackID         string          `json:"pack_id"`
	Revision       int64           `json:"revision"`
	ConsumerWorkID string          `json:"consumer_work_id"`
	UseRole        ResearchUseRole `json:"use_role"`
	Required       bool            `json:"required"`
	AcceptedAt     string          `json:"accepted_at"`
}

type ResearchRevision struct {
	PackID    string `json:"pack_id"`
	Revision  int64  `json:"revision"`
	Question  string `json:"question"`
	ScopeIn   string `json:"scope_in"`
	ScopeOut  string `json:"scope_out"`
	DoneWhen  string `json:"done_when"`
	Method    string `json:"method"`
	CreatedAt string `json:"created_at"`
	// Freshness is this revision's authoritative state (issue #122).
	Freshness ResearchFreshness `json:"freshness"`
	Findings  []ResearchFinding `json:"findings,omitempty"`
	Sources   []ResearchSource  `json:"sources,omitempty"`
}

type ResearchPack struct {
	PackID          string             `json:"pack_id"`
	OwnerWorkID     string             `json:"owner_work_id"`
	CurrentRevision int64              `json:"current_revision"`
	Freshness       ResearchFreshness  `json:"freshness"`
	ExpectedVersion int64              `json:"expected_version"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
	Revisions       []ResearchRevision `json:"revisions,omitempty"`
	Consumers       []ResearchConsumer `json:"consumers,omitempty"`
}

type CreateResearchPackRequest struct {
	Identity    ResearchMutationIdentity `json:"identity"`
	PackID      string                   `json:"pack_id,omitempty"`
	OwnerWorkID string                   `json:"owner_work_id"`
	Revision    ResearchRevisionInput    `json:"revision"`
	Freshness   ResearchFreshness        `json:"freshness,omitempty"`
}

type AppendResearchRevisionRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	ExpectedVersion int64                    `json:"expected_version"`
	Revision        ResearchRevisionInput    `json:"revision"`
}

type ResearchFindingRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	Revision        int64                    `json:"revision,omitempty"`
	ExpectedVersion int64                    `json:"expected_version"`
	Finding         ResearchFinding          `json:"finding"`
}

type ResearchSourceRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	Revision        int64                    `json:"revision,omitempty"`
	ExpectedVersion int64                    `json:"expected_version"`
	Source          ResearchSource           `json:"source"`
}

type BindResearchConsumerRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	Revision        int64                    `json:"revision"`
	ExpectedVersion int64                    `json:"expected_version"`
	Consumer        ResearchConsumer         `json:"consumer"`
}

type UnbindResearchConsumerRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	Revision        int64                    `json:"revision"`
	ExpectedVersion int64                    `json:"expected_version"`
	ConsumerWorkID  string                   `json:"consumer_work_id"`
}

type ResearchPackMutationRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	ExpectedVersion int64                    `json:"expected_version"`
}

type SetResearchFreshnessRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	ExpectedVersion int64                    `json:"expected_version"`
	Freshness       ResearchFreshness        `json:"freshness"`
	// Revision targets the pinned revision whose freshness is set; 0 means
	// the pack's current revision (issue #122).
	Revision int64 `json:"revision,omitempty"`
}

type ResearchFindingSourceRequest struct {
	Identity        ResearchMutationIdentity `json:"identity"`
	PackID          string                   `json:"pack_id"`
	Revision        int64                    `json:"revision"`
	ExpectedVersion int64                    `json:"expected_version"`
	FindingID       string                   `json:"finding_id"`
	SourceID        string                   `json:"source_id"`
}

type ResearchFreshnessResult struct {
	Status  ResearchFreshness `json:"status"`
	Blocked bool              `json:"blocked"`
	Reasons []string          `json:"reasons,omitempty"`
}

type researchResult struct {
	PackID   string            `json:"pack_id"`
	Revision int64             `json:"revision,omitempty"`
	ID       string            `json:"id,omitempty"`
	Count    int               `json:"count,omitempty"`
	Consumer *ResearchConsumer `json:"consumer,omitempty"`
}
