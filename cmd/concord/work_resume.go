package main

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

type workResumeInput struct {
	ProductID    string              `json:"product_id"`
	ProjectID    string              `json:"project_id"`
	WorkID       string              `json:"work_id"`
	IssueKey     string              `json:"issue_key"`
	SessionRef   string              `json:"session_ref"`
	CallEnvelope *agent.CallEnvelope `json:"call_envelope,omitempty"`
}

type workResumeOutput struct {
	SchemaVersion string         `json:"schema_version"`
	ProductID     string         `json:"product_id"`
	ProjectID     string         `json:"project_id"`
	WorkID        string         `json:"work_id"`
	Worktree      workResumeTree `json:"worktree"`
	// LinearIssue is the locally recorded identity, not remote planning state.
	LinearIssue *linearIssueSection `json:"linear_issue,omitempty"`
	// BranchFreshness reports how far the worktree's branch sits behind the
	// origin default branch after one bounded refresh. It never blocks the
	// resume: a fetch or probe failure reports the typed unknown with no
	// count, and the resume itself still succeeds.
	BranchFreshness *store.BranchFreshness `json:"branch_freshness"`
	// ProjectHandoff carries the handoff addressed to this Project
	// (CD-0182 amendment) while it stands unconsumed, or the resuming
	// session's own consumed bind when the resume names that session. It
	// names the bounded repository job the receiving session must consume
	// through concord_work_transition.project_handoff_consume before managed
	// execution; visibility here never consumes or authorizes. Nil when no
	// handoff addresses the Project under the active contract.
	ProjectHandoff *projectHandoffSection `json:"project_handoff,omitempty"`
}

type linearIssueSection struct {
	HumanKey        string `json:"human_key"`
	RemoteIssueUUID string `json:"remote_issue_uuid"`
	URL             string `json:"url"`
}

type workResumeTree struct {
	workBootstrapTree
	store.WorktreeRestoration
}

// projectHandoffSection is the boot/resume projection of one addressed
// handoff: the bounded job, its durable identity, and the exact next action.
type projectHandoffSection struct {
	HandoffID       string   `json:"handoff_id"`
	SourceProjectID string   `json:"source_project_id"`
	BoundedJob      string   `json:"bounded_job"`
	Changes         []string `json:"changes"`
	Verification    []string `json:"verification"`
	ArtifactRefs    []string `json:"artifact_refs"`
	Blockers        []string `json:"blockers"`
	NextAction      string   `json:"next_action"`
	RecordedAt      string   `json:"recorded_at"`
}

// workResumeRefusalExit is the typed exit status work-resume reports for a
// deterministic refusal: invalid input, a Project the invocation does not
// resolve to, an origin or default-branch check that fails the same way until
// state changes, or a typed store failure the store marks unsafe to repeat.
// Callers classify by this status alone, never by stderr text; the work-resume
// commandSpecs entry declares it. Store failures classify through the shared
// storeFailureExit classifier work-bootstrap owns.
const workResumeRefusalExit = 2

// runWorkResume resolves the worktree a session enters when it resumes an
// existing work item by work identity (issue #891). It reads an active entry
// first and keeps that path read-only. If no entry exists, it applies the
// bootstrap origin gate and durably creates the missing canonical worktree
// under the existing work identity.
func runWorkResume(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input workResumeInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
		return workResumeRefusalExit
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.ProjectID) {
		writeOperatorDiagnostic(errOut, "work-resume", "product_id and project_id are required")
		return workResumeRefusalExit
	}
	if (input.WorkID == "") == (input.IssueKey == "") {
		writeOperatorDiagnostic(errOut, "work-resume", "exactly one of work_id or issue_key is required")
		return workResumeRefusalExit
	}
	if input.WorkID != "" && !sessionPrepareID.MatchString(input.WorkID) {
		writeOperatorDiagnostic(errOut, "work-resume", "work_id must be a valid work identity")
		return workResumeRefusalExit
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", "cannot read invocation directory")
		return 1
	}
	ctx := context.Background()
	ctx, err = workResumeCaller(ctx, s, input, cwd)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
		return storeFailureExit(err)
	}
	resolution, err := s.ResolveProject(ctx, cwd, cwd)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
		return storeFailureExit(err)
	}
	if resolution.ProjectID != input.ProjectID {
		writeOperatorDiagnostic(errOut, "work-resume", "invocation must resolve to the requested Project")
		return workResumeRefusalExit
	}
	if input.IssueKey != "" {
		link, err := s.ReadLinearLinkByIssueKey(ctx, input.IssueKey)
		if err != nil {
			writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
			return storeFailureExit(err)
		}
		input.WorkID = link.WorkID
	}
	entry, err := s.ResumeWorktreeLocation(ctx, input.ProductID, input.ProjectID, input.WorkID)
	if err != nil {
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.Kind != store.KindProjectionNotFound {
			writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
			return storeFailureExit(err)
		}
		// A missing active tree may bootstrap; removed execution may not.
		entry, err = bootstrapResumeWorktree(ctx, s, input, resolution)
		if err != nil {
			writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
			return storeFailureExit(err)
		}
	}
	// The target is derived before the origin gate: a session that already
	// runs in the item's own worktree chains from no origin, so the move is
	// the convergent no-op and the dirty, lease, and worker guards apply only
	// to a resume that leaves a different worktree.
	if !resolution.MainWorktree && !samePath(cwd, entry.Path) {
		if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
			writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(unclaimedOriginHint(err, "resume work "+input.WorkID+" by running work_start from its active worktree "+entry.Path)))
			return storeFailureExit(err)
		}
	}
	return finishWorkResume(ctx, s, input, entry, out, errOut)
}

func bootstrapResumeWorktree(ctx context.Context, s *store.Store, input workResumeInput, resolution store.ProjectResolution) (store.WorktreeEntry, error) {
	if _, err := s.ReadWorkItemSummary(ctx, input.WorkID); err != nil {
		return store.WorktreeEntry{}, err
	}
	var ref string
	if !resolution.MainWorktree {
		if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
			return store.WorktreeEntry{}, unclaimedOriginHint(err, "work "+input.WorkID+" has no active worktree; run work-resume from the default checkout "+resolution.Repository.CanonicalPath+" to create it")
		}
		var err error
		ref, err = store.DefaultBranchRef(ctx, resolution.Repository.CanonicalPath)
		if err != nil {
			return store.WorktreeEntry{}, err
		}
	}
	result, err := s.BootstrapExistingWorktree(ctx, store.ExistingBootstrapRequest{
		ProductID: input.ProductID, ProjectID: input.ProjectID, WorkID: input.WorkID, Ref: ref, SessionRef: input.SessionRef,
	}, nil)
	if err != nil {
		return store.WorktreeEntry{}, err
	}
	return result.Entry, nil
}

func finishWorkResume(ctx context.Context, s *store.Store, input workResumeInput, entry store.WorktreeEntry, out, errOut io.Writer) int {
	restoration, err := s.RestoreResumeWorktree(ctx, input.WorkID, entry, nil)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
		return storeFailureExit(err)
	}
	output := workResumeOutput{
		SchemaVersion: "1.0", ProductID: input.ProductID, ProjectID: input.ProjectID, WorkID: input.WorkID,
		Worktree: workResumeTree{
			workBootstrapTree:   workBootstrapTree{SetID: entry.SetID, Branch: entry.Branch, BaseSHA: entry.BaseSHA, Path: entry.Path, State: entry.State},
			WorktreeRestoration: restoration,
		},
	}
	link, err := s.ReadLinearLink(ctx, input.WorkID)
	if err != nil {
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.Kind != store.KindUnknownScope {
			writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
			return storeFailureExit(err)
		}
	} else {
		output.LinearIssue = &linearIssueSection{HumanKey: link.HumanKey, RemoteIssueUUID: link.RemoteIssueUUID, URL: link.URL}
	}
	// The freshness sample runs after the worktree read or bootstrap has
	// fully succeeded: the bounded refresh reports how far the branch sits
	// behind the origin default branch so the agent rebases deliberately. It
	// never rebases and never blocks the resume.
	freshness := store.SampleWorktreeFreshness(ctx, entry, store.ExecGitRunner{})
	output.BranchFreshness = &freshness
	// The Project-selected boot/resume flow names the bounded job: the
	// recorded handoff addressed to this Project rides the answer so the
	// receiving session consumes it without the operator copying context.
	// The resume's authenticated session reference also re-renders this
	// session's own consumed bind, so a replay after a lost consume response
	// recovers the bounded job instead of booting without it. A read failure
	// degrades the section to nil-grade absence only for a typed not-found;
	// other failures refuse the resume.
	handoff, err := store.ReadPendingProjectHandoffForProject(ctx, s, input.WorkID, input.ProjectID, input.SessionRef)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", diagnosticText(err))
		return storeFailureExit(err)
	}
	if handoff != nil {
		output.ProjectHandoff = &projectHandoffSection{
			HandoffID: handoff.HandoffID, SourceProjectID: handoff.SourceProjectID,
			BoundedJob: handoff.BoundedJob, Changes: handoff.Changes, Verification: handoff.Verification,
			ArtifactRefs: handoff.ArtifactRefs, Blockers: handoff.Blockers,
			NextAction: handoff.NextAction, RecordedAt: handoff.RecordedAt,
		}
	}
	return writeJSON(out, output, errOut)
}

// Resume reuses invoke's envelope and authorizer. A raw session selector never
// supplies authenticated client authority.
func workResumeCaller(ctx context.Context, s *store.Store, input workResumeInput, cwd string) (context.Context, error) {
	if input.CallEnvelope == nil {
		return ctx, nil
	}
	env := *input.CallEnvelope
	refuse := func(detail string) (context.Context, error) {
		return ctx, &store.Failure{Kind: store.KindUnauthorized, Op: "work_resume", Detail: detail, RecoveryAction: "resume with the original authenticated call_envelope"}
	}
	if env.SchemaVersion != "1.0" || env.RequestID == "" || env.ClientRef == "" || env.AgentRef == "" || env.SessionRef == "" || env.ScopeVersion == "" {
		return refuse("call_envelope is missing the invoke identity or context")
	}
	if env.SessionRef != input.SessionRef || env.AmbientProjectID != input.ProjectID || env.SelectedProductID != input.ProductID || !samePath(env.Directory, cwd) || !samePath(env.Worktree, cwd) {
		return refuse("resume selectors differ from the authenticated call_envelope")
	}
	service := agent.NewService(s)
	service.ProjectHostProber = func(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
		return store.ResolveProjectHost(ctx, directory, worktree, store.ExecGitRunner{})
	}
	service.ProjectHostMatcher = func(ctx context.Context, _ *store.Transaction, host store.ResolvedProjectHost) (store.ProjectResolution, error) {
		return s.MatchResolvedProjectHost(ctx, host)
	}
	_, err := service.Authorize(ctx, agent.Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: "product_read", ProductID: input.ProductID, ProjectID: input.ProjectID})
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) {
			return ctx, err
		}
		return refuse(err.Error())
	}
	return ctx, nil
}
