package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/workflowproof"
)

// Completion evidence is collected outside write transactions through
// Service.ExternalEvidenceCommand and bound to the Project's git_remote.

const (
	OutsideRepairModeCompleted = "completed"
	OutsideRepairModeResume    = "resume"

	outsideRepairRefreshedIntent = "refresh_outside_repair"
)

// boundedExternalEvidenceWait caps one external command run while the runtime
// budget still bounds the whole operation on its dispatch context.
const boundedExternalEvidenceWait = 300 * time.Second

var (
	outsideRepairRepositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}/[a-z0-9][a-z0-9._-]{0,99}$`)
	outsideRepairReleaseTagPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

func outsideRepairReleaseTagBound(tag string) bool {
	return outsideRepairReleaseTagPattern.MatchString(tag)
}

func outsideRepairPullRequestsBound(numbers []int64) bool {
	if len(numbers) == 0 || len(numbers) > 32 {
		return false
	}
	seen := map[int64]bool{}
	for _, number := range numbers {
		if number < 1 || seen[number] {
			return false
		}
		seen[number] = true
	}
	return true
}

// DefaultExternalEvidenceCommand runs the GitHub CLI by exact argv with
// a bounded wait; never a shell command string. Production installs it as the
// Service.ExternalEvidenceCommand; tests install a deterministic fake.
func DefaultExternalEvidenceCommand(ctx context.Context, command string, args ...string) (string, error) {
	if command != "gh" {
		return "", fmt.Errorf("external evidence requires the GitHub CLI")
	}
	run, cancel := context.WithTimeout(ctx, boundedExternalEvidenceWait)
	defer cancel()
	process := exec.CommandContext(run, "gh", args...) //nolint:gosec // gh is fixed; the evidence collector supplies read-only argv, and values stay separate without a shell.
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	runErr := process.Run()
	if runErr == nil && run.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("external command %s %s exceeded the bounded wait", command, strings.Join(args, " "))
	}
	if runErr != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("external command %s %s failed: %v: %s", command, strings.Join(args, " "), runErr, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// outsideRepairCommand runs one external command through the injected seam.
func (r runtime) outsideRepairCommand(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("external command carries no argv")
	}
	if r.Authority == nil {
		return "", fmt.Errorf("the external evidence boundary is not installed")
	}
	if r.Authority.ExternalEvidenceCommand == nil {
		return "", fmt.Errorf("external evidence command is not installed")
	}
	return r.Authority.ExternalEvidenceCommand(ctx, args[0], args[1:]...)
}

type outsideRepairDispositionInput struct {
	WorkID          string `json:"work_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type outsideRepairReconcileInput struct {
	WorkID          string  `json:"work_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Reason          string  `json:"reason"`
	Mode            string  `json:"mode"`
	ReleaseTag      string  `json:"release_tag"`
	PullRequests    []int64 `json:"pull_requests"`
	IdempotencyKey  string  `json:"idempotency_key"`
}

// outsideRepairLiveWork reads the work item's lifecycle for the hold through
// the store's own lightweight read. It deliberately avoids WorkPin: a work
// item with no workflow instance (imported work) carries no pin row, yet the
// hold must stay recordable on imported work exactly as the store fold admits
// it (needed or in_progress).
func (r runtime) outsideRepairLiveWork(ctx context.Context, base agentcontract.Envelope, workID string) agentcontract.Envelope {
	lifecycle, err := r.Store.WorkLifecycle(ctx, workID)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
			return coreError(base, "invalid_input", "outside repair selects a live work item", "reread_entities", false)
		}
		return failureEnvelope(base, err)
	}
	if lifecycle != "needed" && lifecycle != "in_progress" {
		// The store fold repeats this bound; the challenge is never offered
		// on closed work.
		return coreError(base, "invalid_transition", "outside repair requires needed or in_progress work", "reread_entities", false)
	}
	return agentcontract.Envelope{}
}

// outsideRepairHoldWorkPin reads the work pin the reconcile binds against. A
// hold makes the pin readable even on instance-less work (the store's pin
// short-circuit), so a projection-not-found refusal here names the missing
// hold, and every other failure is a genuine read refusal.
func (r runtime) outsideRepairHoldWorkPin(ctx context.Context, base agentcontract.Envelope, workID string) (store.WorkPin, agentcontract.Envelope) {
	pin, err := store.ReadWorkPin(ctx, r.Store, workID)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
			return store.WorkPin{}, coreError(base, "invalid_input", "outside repair reconcile requires an active outside-repair hold", "reread_entities", false)
		}
		return store.WorkPin{}, failureEnvelope(base, err)
	}
	if pin.Lifecycle != "needed" && pin.Lifecycle != "in_progress" {
		return store.WorkPin{}, coreError(base, "invalid_transition", "outside repair requires needed or in_progress work", "reread_entities", false)
	}
	return pin, agentcontract.Envelope{}
}

// outsideRepairRepositoryForProject resolves the Project's recorded GitHub
// git_remote locator. The caller never supplies the repository, and every
// forge URL the receipt names is constructed from the resolved owner/repo.
func (r runtime) outsideRepairRepositoryForProject(ctx context.Context, base agentcontract.Envelope, projectID string) (string, agentcontract.Envelope) {
	locators, err := r.Store.ProjectLocators(ctx, projectID)
	if err != nil {
		return "", failureEnvelope(base, err)
	}
	for _, locator := range locators {
		if locator.Kind != store.LocatorGitRemote {
			continue
		}
		repository, ok := outsideRepairGitHubRepository(locator.NormalizedValue)
		if ok {
			return repository, agentcontract.Envelope{}
		}
	}
	return "", coreError(base, "invalid_input", "outside repair closes a Project whose registered git origin is not a GitHub repository", "reread_entities", false)
}

func outsideRepairGitHubRepository(normalizedRemote string) (string, bool) {
	parsed, err := url.Parse(normalizedRemote)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if host == "github.com" {
		if !outsideRepairRepositoryPattern.MatchString(path) {
			return "", false
		}
		return path, true
	}
	return "", false
}

// outsideRepairEvidenceForPlan admits the registered repository and delegates
// completion proof before any write transaction opens.
func (r runtime) outsideRepairEvidenceForPlan(ctx context.Context, base agentcontract.Envelope, pin store.WorkPin, in outsideRepairReconcileInput, authorityRef string) (workflowproof.Evidence, agentcontract.Envelope) {
	normalized, refusal := r.outsideRepairRepositoryForProject(ctx, base, pin.ProjectID)
	if refusal.Error != nil {
		return workflowproof.Evidence{}, refusal
	}
	evidence, proofRefusal := workflowproof.Collect(ctx, workflowproof.Request{
		Repository: normalized, PullRequests: in.PullRequests, ReleaseTag: in.ReleaseTag,
		LinearIssueKey: pin.LinearIssueKey, AuthorityRef: authorityRef,
		Command: r.outsideRepairCommand, Now: func() time.Time { return r.Authority.now() },
	})
	return evidence, outsideRepairProofRefusal(base, proofRefusal)
}

func outsideRepairProofRefusal(base agentcontract.Envelope, refusal *workflowproof.Refusal) agentcontract.Envelope {
	if refusal == nil {
		return agentcontract.Envelope{}
	}
	if refusal.ForeignIdentity() {
		return coreError(base, "unauthorized", refusal.Error(), "contact_operator", false)
	}
	return coreError(base, "missing_evidence", refusal.Error(), "provide_evidence", false)
}

// planOutsideRepairDisposition plans the hold: durably record the
// outside-repair disposition on live work. No workflow pin
// and no managed step is required to enter the route.
func (r runtime) planOutsideRepairDisposition(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in outsideRepairDispositionInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if liveWorkRefusal := r.outsideRepairLiveWork(ctx, base, in.WorkID); liveWorkRefusal.Error != nil {
		return liveWorkRefusal, nil, true
	}
	if refusal, handled := r.prepareOutsideRepairPlan(base, plan, in.WorkID, in.Reason, "", "", nil); handled {
		return refusal, nil, true
	}
	plan.effect = r.outsideRepairPlanEffect(digest, in.WorkID, in.ExpectedVersion, in.Reason, "", nil)
	return agentcontract.Envelope{}, nil, false
}

// planOutsideRepairReconcile plans the one reconcile action: completed mode
// closes the held work from collected evidence, resume mode clears the hold.
func (r runtime) planOutsideRepairReconcile(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, grant Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in outsideRepairReconcileInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	switch in.Mode {
	case OutsideRepairModeCompleted:
		if in.ReleaseTag == "" || len(in.PullRequests) == 0 {
			return coreError(base, "invalid_input", "a completed reconcile names its published release tag and merged pull requests", "reread_entities", false), nil, true
		}
		if !outsideRepairReleaseTagBound(in.ReleaseTag) || !outsideRepairPullRequestsBound(in.PullRequests) {
			return coreError(base, "invalid_input", "release_tag accepts 1..128 characters of [A-Za-z0-9._-] and pull_requests accepts 1..32 unique positive numbers", "reread_entities", false), nil, true
		}
	case OutsideRepairModeResume:
	default:
		return coreError(base, "invalid_input", "outside repair reconcile accepts mode completed or resume", "reread_entities", false), nil, true
	}
	pin, pinRefusal := r.outsideRepairHoldWorkPin(ctx, base, in.WorkID)
	if pinRefusal.Error != nil {
		return pinRefusal, nil, true
	}
	// CD-0243 D3/D7 remove ordinary consent, not authenticated completion
	// proof. Fetch it once before any transaction opens.
	var evidence *workflowproof.Evidence
	if in.Mode == OutsideRepairModeCompleted {
		actorRef, err := store.WorkflowActorRef(store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent})
		if err != nil {
			return failureEnvelope(base, err), nil, true
		}
		collected, collectRefusal := r.outsideRepairEvidenceForPlan(ctx, base, pin, in, actorRef)
		if collectRefusal.Error != nil {
			return collectRefusal, nil, true
		}
		evidence = &collected
	}
	if refusal, handled := r.prepareOutsideRepairPlan(base, plan, in.WorkID, in.Reason, in.Mode, in.ReleaseTag, in.PullRequests); handled {
		return refusal, nil, true
	}
	plan.effect = r.outsideRepairPlanEffect(digest, in.WorkID, in.ExpectedVersion, in.Reason, in.Mode, evidence)
	return agentcontract.Envelope{}, nil, false
}

func (r runtime) prepareOutsideRepairPlan(base agentcontract.Envelope, plan *mutationPlan, workID string, reason string, mode, releaseTag string, pullRequests []int64) (agentcontract.Envelope, bool) {
	if len([]rune(reason)) < 2 || len([]rune(reason)) > 4096 {
		return coreError(base, "invalid_input", "outside repair requires a bounded repair reason", "reread_entities", false), true
	}
	plan.scope["work_ids"] = []string{workID}
	if mode == OutsideRepairModeCompleted {
		plan.scope["release_tag"] = releaseTag
		selectors := make([]string, 0, len(pullRequests))
		for _, number := range pullRequests {
			selectors = append(selectors, strconv.FormatInt(number, 10))
		}
		plan.scope["pull_requests"] = selectors
	}
	return agentcontract.Envelope{}, false
}

// outsideRepairPlanEffect attributes coordination to the caller.
func (r runtime) outsideRepairPlanEffect(digest string, workID string, expectedVersion int64, reason, mode string, evidence *workflowproof.Evidence) mutationEffect {
	return func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		request := store.OutsideRepairRequest{
			Actor:           store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent},
			WorkID:          workID,
			Reason:          reason,
			EventID:         digest + ":outside-repair",
			ExpectedVersion: expectedVersion,
			OccurredAt:      r.Authority.now(),
		}
		var result store.ApplyOperationResult
		var err error
		switch mode {
		case OutsideRepairModeCompleted:
			if evidence == nil {
				return nil, nil, nil, fmt.Errorf("completed reconcile collected no evidence")
			}
			result, err = store.ReconcileOutsideRepairTx(ctx, tx, store.OutsideRepairReconcileRequest{OutsideRepairRequest: request, Evidence: *evidence})
		case OutsideRepairModeResume:
			result, err = store.ResumeOutsideRepairTx(ctx, tx, request)
		default:
			result, err = store.SetOutsideRepairDispositionTx(ctx, tx, request)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(expectedVersion+2, 10)}}
		var intents []agentcontract.NextIntent
		if mode == OutsideRepairModeResume {
			intents = []agentcontract.NextIntent{{Tool: "concord_work_browse", Operation: "scope", ReasonCode: outsideRepairRefreshedIntent, RequiredFields: []string{"work_id"}}}
		}
		return mutationPayload(changed, intents), result.EventIDs, changed, nil
	}
}
