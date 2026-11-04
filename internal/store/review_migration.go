package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

func legacyReviewDigest(value any) string {
	raw, _ := json.Marshal(value)
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type legacyReviewCandidate struct {
	ProjectID string `json:"project_id"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree"`
}

type legacyReviewCriterion struct {
	PredicateID   string `json:"predicate_id"`
	ContentDigest string `json:"content_digest"`
}

type legacyReviewDependencies struct {
	Criteria                  []legacyReviewCriterion `json:"criteria"`
	ArchitectureDigest        string                  `json:"architecture_digest,omitempty"`
	ArchitectureNotApplicable bool                    `json:"architecture_not_applicable"`
}

type legacyReviewAssessment struct {
	PredicateID string   `json:"predicate_id"`
	Assessment  string   `json:"assessment"`
	ReceiptRefs []string `json:"receipt_refs"`
}

type legacyReviewObligation struct {
	ObligationID string `json:"obligation_id"`
	OwnerRef     string `json:"owner_ref"`
	SubjectRef   string `json:"subject_ref"`
	InputDigest  string `json:"input_digest"`
	NeededProof  string `json:"needed_proof"`
}

type legacyReviewFinding struct {
	FindingID    string                     `json:"finding_id"`
	Severity     string                     `json:"severity"`
	Confidence   string                     `json:"confidence"`
	Detail       string                     `json:"detail"`
	InScope      bool                       `json:"in_scope"`
	Blocking     bool                       `json:"blocking"`
	Status       string                     `json:"status"`
	EvidenceRefs []string                   `json:"evidence_refs"`
	ClosureRefs  []string                   `json:"closure_refs"`
	Oracle       *legacyReviewOracleFinding `json:"oracle,omitempty"`
}

type legacyReviewOracleFinding struct {
	Classification     string                         `json:"classification"`
	OwnerID            string                         `json:"owner_id,omitempty"`
	FailureFamily      string                         `json:"failure_family,omitempty"`
	PredicateIDs       []string                       `json:"predicate_ids,omitempty"`
	LawBindings        []legacyReviewOracleLawBinding `json:"law_bindings,omitempty"`
	CaseIDs            []string                       `json:"case_ids,omitempty"`
	ControlIDs         []string                       `json:"control_ids,omitempty"`
	EvidenceRefs       []string                       `json:"evidence_refs,omitempty"`
	EntryPoint         string                         `json:"entry_point,omitempty"`
	ContinuesFindingID string                         `json:"continues_finding_id,omitempty"`
	VariantOf          string                         `json:"variant_of,omitempty"`
}

type legacyReviewOracleLawSource struct {
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	LawID       string `json:"law_id"`
	ContentHash string `json:"content_hash"`
}

type legacyReviewOracleLawBinding struct {
	Source legacyReviewOracleLawSource `json:"source"`
	Clause string                      `json:"clause"`
}

type legacyReviewResolution struct {
	FindingID     string   `json:"finding_id"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
	JudgedCaseIDs []string `json:"judged_case_ids,omitempty"`
}

type legacyReviewBlock struct {
	Disposition          string                   `json:"disposition"`
	InputCandidate       legacyReviewCandidate    `json:"input_candidate"`
	OutputCandidate      legacyReviewCandidate    `json:"output_candidate"`
	Dependencies         legacyReviewDependencies `json:"dependencies"`
	Assessments          []legacyReviewAssessment `json:"assessments"`
	Findings             []legacyReviewFinding    `json:"findings"`
	RemainingObligations []legacyReviewObligation `json:"remaining_obligations"`
	ResolvedFindings     []legacyReviewResolution `json:"resolved_findings,omitempty"`
}

type legacyAcceptedReview struct {
	ReportRef    string            `json:"report_ref"`
	ReportDigest string            `json:"report_digest"`
	ActorRef     string            `json:"actor_ref"`
	Report       legacyReviewBlock `json:"report"`
}

type legacyReviewFindingSource struct {
	CanonicalID string          `json:"canonical_id"`
	DomainID    string          `json:"domain_id"`
	EventID     string          `json:"event_id"`
	Kind        string          `json:"kind"`
	Seq         int64           `json:"seq"`
	Ordinal     int             `json:"ordinal"`
	Oracle      json.RawMessage `json:"oracle,omitempty"`
}

type legacyReviewState struct {
	Candidate *legacyReviewCandidate               `json:"candidate"`
	Accepted  *legacyAcceptedReview                `json:"accepted"`
	Findings  []legacyReviewFinding                `json:"findings"`
	Sources   map[string]legacyReviewFindingSource `json:"finding_sources,omitempty"`
}

func legacyReviewFindingMap(state legacyReviewState) map[string]legacyReviewFinding {
	findings := map[string]legacyReviewFinding{}
	for _, finding := range state.Findings {
		findings[finding.FindingID] = finding
	}
	return findings
}

func setLegacyReviewFindings(state *legacyReviewState, findings map[string]legacyReviewFinding) {
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state.Findings = make([]legacyReviewFinding, 0, len(ids))
	for _, id := range ids {
		state.Findings = append(state.Findings, findings[id])
	}
}

func writeLegacyReviewFacts(ctx context.Context, q migrationRunner, work string, state legacyReviewState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_review_state(work_id,state) VALUES(?,?) ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, work, string(raw))
	return err
}

type legacyReviewJob struct {
	JobID    string `json:"job_id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type legacyReviewAuthorization struct {
	Capability string           `json:"worker_capability_class"`
	Job        *legacyReviewJob `json:"worker_job"`
	Retained   *struct {
		State struct {
			Candidate legacyReviewCandidate `json:"candidate"`
		} `json:"state"`
		Input legacyReviewCandidate `json:"input"`
	} `json:"worker_retained_review"`
}

type legacyReviewResult struct {
	Attempt         string             `json:"attempt_id"`
	Job             *legacyReviewJob   `json:"worker_job"`
	ContextFindings []json.RawMessage  `json:"context_findings"`
	Review          *legacyReviewBlock `json:"review"`
	Observed        *struct {
		Commit string `json:"commit"`
		Tree   string `json:"tree"`
		State  string `json:"state"`
	} `json:"observed_delivery"`
}

type legacyReviewRecord struct {
	work, kind, actor, ref string
	seq                    int64
	raw                    []byte
}

func readLegacyReviewRecords(ctx context.Context, q migrationRunner) ([]legacyReviewRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.subject_id,e.kind,e.actor,e.event_id,e.seq,e.payload FROM domain_events e JOIN work_items w ON w.id=e.subject_id WHERE e.subject_type='work_item' AND e.kind IN ('workflow.action_completed','worker.completed') ORDER BY e.subject_id,e.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []legacyReviewRecord
	for rows.Next() {
		var r legacyReviewRecord
		if err := rows.Scan(&r.work, &r.kind, &r.actor, &r.ref, &r.seq, &r.raw); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Migration 148 reads the historical log before migration 151 removes it.
// Only the fields needed to retain accepted subjects and findings are decoded.
func backfillWorkflowReviewFacts(ctx context.Context, q migrationRunner) error {
	records, err := readLegacyReviewRecords(ctx, q)
	if err != nil {
		return err
	}
	states := map[string]legacyReviewState{}
	auths := map[string]legacyReviewAuthorization{}
	results := map[string]legacyReviewResult{}
	refs := map[string]string{}
	for _, r := range records {
		state := states[r.work]
		keyPrefix := r.work + "\x00"
		if r.kind == "worker.completed" {
			var result legacyReviewResult
			if err := json.Unmarshal(r.raw, &result); err != nil {
				return err
			}
			if err := retainLegacyReviewSources(ctx, q, r.work, &state, result, r.ref, r.seq); err != nil {
				return err
			}
			key := keyPrefix + result.Attempt
			results[key] = result
			auth := auths[key]
			if result.Review != nil && auth.Capability == "review" {
				refs[key] = r.ref
				observeLegacyReviewReport(&state, result.Review, auth.Retained != nil && state.Candidate != nil && *state.Candidate == auth.Retained.State.Candidate && result.Review.InputCandidate == auth.Retained.Input)
			}
		} else {
			var action struct {
				ID      string `json:"action_id"`
				Attempt string `json:"worker_attempt_id"`
				legacyReviewAuthorization
			}
			if err := json.Unmarshal(r.raw, &action); err != nil {
				return err
			}
			key := keyPrefix + action.Attempt
			if action.ID == "dispatch_worker" {
				auths[key] = action.legacyReviewAuthorization
			}
			if result, found := results[key]; found && action.ID == "accept_worker_result" {
				if err := acceptLegacyReviewResult(ctx, q, r.work, r.actor, &state, result, auths[key], refs[key]); err != nil {
					return err
				}
			}
		}
		states[r.work] = state
	}
	for work, state := range states {
		if err := writeLegacyReviewFacts(ctx, q, work, state); err != nil {
			return err
		}
	}
	return nil
}

// Preserve minting coordinates and the exact recorded job's owner inventory
// before the log retires. Later reports cannot reassign a retained local ID.
func retainLegacyReviewSources(ctx context.Context, q migrationRunner, work string, state *legacyReviewState, result legacyReviewResult, ref string, seq int64) error {
	if result.Review == nil || len(result.Review.Findings) == 0 {
		return nil
	}
	oracle, owners, err := legacyReviewSourceOracle(ctx, q, work, result.Job)
	if err != nil {
		return err
	}
	if state.Sources == nil {
		state.Sources = map[string]legacyReviewFindingSource{}
	}
	for index, finding := range result.Review.Findings {
		if _, retained := state.Sources[finding.FindingID]; retained || finding.FindingID == "" {
			continue
		}
		ordinal := len(result.ContextFindings) + index
		source := legacyReviewFindingSource{CanonicalID: fmt.Sprintf("finding:%d:%d", seq, ordinal), DomainID: "unresolved", EventID: ref, Kind: "worker.completed", Seq: seq, Ordinal: ordinal, Oracle: oracle}
		if finding.Oracle != nil && oracle != nil {
			for _, owner := range owners {
				if owner.OwnerID == finding.Oracle.OwnerID && owner.DomainID != "" {
					source.DomainID = owner.DomainID
				}
			}
		}
		state.Sources[finding.FindingID] = source
	}
	return nil
}

type legacyReviewOracleOwner struct {
	OwnerID   string `json:"owner_id"`
	DomainID  string `json:"domain_id"`
	Mechanism struct {
		ProjectID  string `json:"project_id"`
		Path       string `json:"path"`
		EntryPoint string `json:"entry_point"`
	} `json:"mechanism"`
	Obligation   string   `json:"obligation"`
	PredicateIDs []string `json:"predicate_ids"`
	LawBindings  []struct {
		Source WorkContextReadingSource `json:"source"`
		Clause string                   `json:"clause"`
	} `json:"law_bindings"`
}

type legacyReviewOracleCase struct {
	CaseID        string   `json:"case_id"`
	OwnerID       string   `json:"owner_id"`
	EntryPoint    string   `json:"entry_point"`
	InputClass    string   `json:"input_class"`
	ExpectedState string   `json:"expected_state"`
	ControlIDs    []string `json:"control_ids"`
	Judge         string   `json:"judge"`
}

type legacyReviewOracleControl struct {
	ControlID             string                   `json:"control_id"`
	OwnerID               string                   `json:"owner_id"`
	PredicateIDs          []string                 `json:"predicate_ids"`
	CaseIDs               []string                 `json:"case_ids"`
	RecipeSource          WorkContextReadingSource `json:"recipe_source"`
	Argv                  []string                 `json:"argv"`
	Cwd                   string                   `json:"cwd"`
	ExpectedResult        string                   `json:"expected_result"`
	RequiredEvidenceRole  string                   `json:"required_evidence_role"`
	ReadinessEvidenceRefs []string                 `json:"readiness_evidence_refs"`
}

func legacyReviewSourceOracle(ctx context.Context, q queryer, work string, job *legacyReviewJob) (json.RawMessage, []legacyReviewOracleOwner, error) {
	if job == nil {
		return nil, nil, nil
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(recorded_payload,'$.acceptance_oracle'),'null') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, work, job.JobID, job.Revision, job.Digest).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var oracle *struct {
		Owners   []legacyReviewOracleOwner   `json:"owners"`
		Cases    []legacyReviewOracleCase    `json:"cases"`
		Controls []legacyReviewOracleControl `json:"controls"`
	}
	if err := json.Unmarshal([]byte(raw), &oracle); err != nil {
		return nil, nil, err
	}
	if oracle == nil {
		return nil, nil, nil
	}
	return json.RawMessage(raw), oracle.Owners, nil
}

func acceptLegacyReviewResult(ctx context.Context, q migrationRunner, work, actor string, state *legacyReviewState, result legacyReviewResult, auth legacyReviewAuthorization, ref string) error {
	if result.Review != nil && auth.Capability == "review" {
		findings := legacyReviewFindingMap(*state)
		// Historical acceptance only applies proposed closures. Observation
		// already merged findings; accepting an older report cannot reopen them.
		for _, finding := range result.Review.Findings {
			if finding.Status == "closed" {
				previous := findings[finding.FindingID]
				previous.Status, previous.ClosureRefs = "closed", finding.ClosureRefs
				findings[finding.FindingID] = previous
			}
		}
		state.Accepted = &legacyAcceptedReview{ReportRef: ref, ReportDigest: legacyReviewDigest(result.Review), ActorRef: actor, Report: *result.Review}
		candidate := result.Review.OutputCandidate
		state.Candidate = &candidate
		setLegacyReviewFindings(state, findings)
		return nil
	}
	if result.Observed == nil || result.Observed.State != "clean" {
		return nil
	}
	project := ""
	if auth.Job != nil {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(recorded_payload,'$.project_scope'),'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND recorded_payload IS NOT NULL`, work, auth.Job.JobID, auth.Job.Revision, auth.Job.Digest).Scan(&project); err != nil {
			return err
		}
	}
	state.Candidate = &legacyReviewCandidate{ProjectID: project, Commit: result.Observed.Commit, Tree: result.Observed.Tree}
	return nil
}

// observeLegacyReviewReport keeps the historical terminal walk: a report on a
// different input accumulates findings only through its retained base, a
// terminal never closes a finding, and only acceptance installs a candidate.
func observeLegacyReviewReport(state *legacyReviewState, report *legacyReviewBlock, retainedBase bool) {
	if state.Candidate != nil && *state.Candidate != report.InputCandidate && !retainedBase {
		return
	}
	findings := legacyReviewFindingMap(*state)
	for _, finding := range report.Findings {
		if previous, found := findings[finding.FindingID]; found {
			if previous.Oracle != nil {
				finding.Oracle = previous.Oracle
			}
			if previous.Status == "open" {
				finding.InScope = finding.InScope || previous.InScope
				finding.Blocking = finding.Blocking || previous.Blocking
				finding.Status = "open"
			}
		}
		if finding.Status == "closed" {
			finding.Status = "open"
		}
		findings[finding.FindingID] = finding
	}
	setLegacyReviewFindings(state, findings)
}
