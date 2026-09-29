package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

type workResumeInput struct {
	ProductID  string `json:"product_id"`
	ProjectID  string `json:"project_id"`
	WorkID     string `json:"work_id"`
	SessionRef string `json:"session_ref"`
}

type workResumeOutput struct {
	SchemaVersion string               `json:"schema_version"`
	ProductID     string               `json:"product_id"`
	ProjectID     string               `json:"project_id"`
	WorkID        string               `json:"work_id"`
	Worktree      workBootstrapTree    `json:"worktree"`
	LinearRemote  *linearRemoteSection `json:"linear_remote,omitempty"`
}

// linearRemoteStatus is the remote workflow state compared with the
// lifecycle-mapped expected status. mismatch stays false when the Product
// has no mapping for the lifecycle: a missing mapping is not a disagreement.
type linearRemoteStatus struct {
	Expected        string `json:"expected"`
	Actual          string `json:"actual"`
	RemoteStateType string `json:"remote_state_type"`
	Mismatch        bool   `json:"mismatch"`
}

type linearRemoteTitle struct {
	Remote  string `json:"remote"`
	Differs bool   `json:"differs"`
}

type linearRemoteComment struct {
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

type linearRemoteComments struct {
	Items []linearRemoteComment `json:"items"`
	// Truncated reports that more comments exist than the section reports:
	// the connection held a further page, or the encoded budget stopped the
	// page early.
	Truncated bool `json:"truncated"`
	// Reason carries the typed degraded reason when only the comments read
	// failed after the issue itself was read.
	Reason string `json:"reason,omitempty"`
}

// linearRemoteSection is the typed remote Linear state a resume returns for
// an item with a confirmed link in a linear_enabled Product. Degraded
// authority carries only the typed reason; the resume itself still succeeds.
type linearRemoteSection struct {
	Authority string `json:"authority"`
	// Reason is set only when authority is degraded: missing_credentials,
	// unauthorized, rate_limited, timeout, unavailable, or not_found.
	Reason string `json:"reason,omitempty"`
	// The remaining fields are set only when authority is ok.
	ChangedSinceRecorded *bool                 `json:"changed_since_recorded,omitempty"`
	UpdatedAt            *string               `json:"updated_at,omitempty"`
	Status               *linearRemoteStatus   `json:"status,omitempty"`
	Title                *linearRemoteTitle    `json:"title,omitempty"`
	Description          *string               `json:"description,omitempty"`
	DescriptionTruncated *bool                 `json:"description_truncated,omitempty"`
	Comments             *linearRemoteComments `json:"comments,omitempty"`
}

const (
	// linearResumeCommentPage is the fixed comment page a resume reports.
	linearResumeCommentPage = 20
	// linearResumeCommentBodyLimit bounds one comment body's runes.
	linearResumeCommentBodyLimit = 500
	// linearResumeCommentAuthorLimit bounds one author name's runes.
	linearResumeCommentAuthorLimit = 100
	// linearRemoteCommentsBudget bounds the encoded comments list so a full
	// page of multibyte bodies cannot push the resume output past the
	// adapter envelope.
	linearRemoteCommentsBudget = 20 * 1024
	// linearResumeDescriptionLimit bounds the description's runes.
	linearResumeDescriptionLimit = 2000
)

// linearResumeTimeout bounds the whole remote check so the worktree move
// never waits on a third-party service. It is a variable so tests can
// shorten it instead of waiting out the real budget.
var linearResumeTimeout = 10 * time.Second

// checkLinearRemote reads the linked Linear issue once and compares it with
// what Concord last recorded. It applies only to a resume of an item whose
// Product is linear_enabled with a declared connection and whose link is
// confirmed; every other resume returns nil, makes no Linear call, and keeps
// its output byte-identical. The check reads only: resume records nothing to
// the store or to Linear (CD-0104 D1), and CD-0171 D8 keeps the outbox the
// only writer of issue status. A local store read failure also returns nil,
// because the resume outcome must never depend on the remote check.
func checkLinearRemote(ctx context.Context, s *store.Store, workID string) *linearRemoteSection {
	// The owning Product, not the Product the session resumes from, holds
	// the Linear identity and the lifecycle-to-status mapping: a work item
	// shared across Products resumes from a secondary Project too.
	link, err := s.ReadConfirmedLinearResumeLink(ctx, workID)
	if err != nil {
		return nil
	}
	mode, err := s.ResolveLinearPlanningTarget(ctx, link.ProductID)
	if err != nil || mode.PlanningMode != store.PlanningModeLinear {
		return nil
	}
	connection, err := s.ReadLinearConnection(ctx, link.ProductID)
	if err != nil {
		return nil
	}
	client, err := linearclient.FromEnv()
	if err != nil {
		return degradedLinearRemote(err, ctx)
	}
	callCtx, cancel := context.WithTimeout(ctx, linearResumeTimeout)
	defer cancel()
	issue, err := client.GetIssue(callCtx, link.RemoteIssueUUID)
	if err != nil {
		return degradedLinearRemote(err, callCtx)
	}
	comments, hasMore, commentErr := client.ListIssueComments(callCtx, link.RemoteIssueUUID, recordedRemoteTime(link.RemoteUpdatedAt), linearResumeCommentPage)
	section := &linearRemoteSection{Authority: "ok"}
	changed := remoteChangedSinceRecorded(link.RemoteUpdatedAt, issue.UpdatedAt)
	section.ChangedSinceRecorded = &changed
	updatedAt := issue.UpdatedAt.UTC().Format(time.RFC3339Nano)
	section.UpdatedAt = &updatedAt
	expected := connection.StatusIDs[link.Lifecycle]
	section.Status = &linearRemoteStatus{
		Expected:        expected,
		Actual:          issue.StateID,
		RemoteStateType: issue.StateType,
		Mismatch:        expected != "" && linearStatusDiverged(expected, issue.StateID),
	}
	section.Title = &linearRemoteTitle{Remote: issue.Title, Differs: issue.Title != link.Title}
	description, descriptionTruncated := truncateRunes(issue.Description, linearResumeDescriptionLimit)
	section.Description = &description
	section.DescriptionTruncated = &descriptionTruncated
	section.Comments = &linearRemoteComments{Items: []linearRemoteComment{}}
	if commentErr != nil {
		// A comments-read failure after a successful issue read degrades
		// only the comments part and says so.
		section.Comments.Reason = linearRemoteDegradedReason(commentErr, callCtx)
	} else {
		appendBoundedComments(section.Comments, comments, hasMore)
	}
	return section
}

// appendBoundedComments copies the comment page into the section under the
// encoded byte budget, marking the page truncated when the budget stops it
// or the connection held a further page.
func appendBoundedComments(section *linearRemoteComments, comments []linearclient.RemoteComment, hasMore bool) {
	budget := linearRemoteCommentsBudget
	for _, comment := range comments {
		author, _ := truncateRunes(comment.Author, linearResumeCommentAuthorLimit)
		body, _ := truncateRunes(comment.Body, linearResumeCommentBodyLimit)
		item := linearRemoteComment{Author: author, CreatedAt: comment.CreatedAt.UTC().Format(time.RFC3339Nano), Body: body}
		encoded, err := json.Marshal(item)
		if err != nil || len(encoded) > budget {
			section.Truncated = true
			break
		}
		budget -= len(encoded)
		section.Items = append(section.Items, item)
	}
	if hasMore {
		section.Truncated = true
	}
}

// degradedLinearRemote states why the remote check could not read Linear.
func degradedLinearRemote(err error, callCtx context.Context) *linearRemoteSection {
	return &linearRemoteSection{Authority: "degraded", Reason: linearRemoteDegradedReason(err, callCtx)}
}

// linearRemoteDegradedReason maps one linearclient failure onto the typed
// degraded vocabulary. A deadline the check itself set reads as timeout; a
// transport failure inside that window reads as unavailable.
func linearRemoteDegradedReason(err error, callCtx context.Context) string {
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	var failure *linearclient.Failure
	if !errors.As(err, &failure) {
		return "unavailable"
	}
	switch failure.Kind {
	case linearclient.KindMissingCredential:
		return "missing_credentials"
	case linearclient.KindAuthRefused:
		return "unauthorized"
	case linearclient.KindRateLimited:
		return "rate_limited"
	case linearclient.KindGraphqlError:
		if strings.Contains(failure.Detail, "returned no issue") {
			return "not_found"
		}
		return "unavailable"
	default:
		return "unavailable"
	}
}

// recordedRemoteTime parses the recorded remote freshness. An empty or
// unparseable value returns the zero time, which keeps the full comment page
// instead of silently narrowing the window.
func recordedRemoteTime(recorded string) time.Time {
	parsed, err := time.Parse(time.RFC3339, recorded)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// remoteChangedSinceRecorded compares the remote updatedAt with the recorded
// remote freshness by instant, so a formatting-only difference is not drift.
// A missing or unparseable record counts as changed whenever the remote
// carries a timestamp.
func remoteChangedSinceRecorded(recorded string, remote time.Time) bool {
	parsed := recordedRemoteTime(recorded)
	if parsed.IsZero() {
		return !remote.IsZero()
	}
	return !parsed.Equal(remote)
}

// truncateRunes cuts a remote string to limit runes so the section stays
// inside the agent output envelope, and reports whether it cut.
func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

// runWorkResume resolves the worktree a session enters when it resumes an
// existing work item by work identity (issue #891). It reads an active entry
// first and keeps that path read-only. If no entry exists, it applies the
// bootstrap origin gate and durably creates the missing canonical worktree
// under the existing work identity.
func runWorkResume(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input workResumeInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.ProjectID) || !sessionPrepareID.MatchString(input.WorkID) {
		writeOperatorDiagnostic(errOut, "work-resume", "product_id, project_id, and work_id are required")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", "cannot read invocation directory")
		return 1
	}
	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, cwd, cwd)
	if err != nil || resolution.ProjectID != input.ProjectID {
		writeOperatorDiagnostic(errOut, "work-resume", "invocation must resolve to the requested Project")
		return 1
	}
	entry, err := s.ResumeWorktreeLocation(ctx, input.ProductID, input.ProjectID, input.WorkID)
	if err != nil {
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.Kind != store.KindProjectionNotFound {
			writeOperatorDiagnostic(errOut, "work-resume", err.Error())
			return 1
		}
		ref := "HEAD"
		if !resolution.MainWorktree {
			if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
				writeOperatorDiagnostic(errOut, "work-resume", err.Error())
				return 1
			}
			ref, err = store.DefaultBranchRef(ctx, resolution.Repository.CanonicalPath)
			if err != nil {
				writeOperatorDiagnostic(errOut, "work-resume", err.Error())
				return 1
			}
		}
		result, bootstrapErr := s.BootstrapExistingWorktree(ctx, store.ExistingBootstrapRequest{
			ProductID: input.ProductID, ProjectID: input.ProjectID, WorkID: input.WorkID, Ref: ref, SessionRef: input.SessionRef,
		}, nil)
		if bootstrapErr != nil {
			writeOperatorDiagnostic(errOut, "work-resume", bootstrapErr.Error())
			return 1
		}
		entry = result.Entry
	}
	// The target is derived before the origin gate: a session that already
	// runs in the item's own worktree chains from no origin, so the move is
	// the convergent no-op and the dirty, lease, and worker guards apply only
	// to a resume that leaves a different worktree.
	if !resolution.MainWorktree && !samePath(cwd, entry.Path) {
		if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
			writeOperatorDiagnostic(errOut, "work-resume", err.Error())
			return 1
		}
	}
	output := workResumeOutput{
		SchemaVersion: "1.0", ProductID: input.ProductID, ProjectID: input.ProjectID, WorkID: input.WorkID,
		Worktree: workBootstrapTree{SetID: entry.SetID, Branch: entry.Branch, BaseSHA: entry.BaseSHA, Path: entry.Path, State: entry.State},
	}
	// The remote check runs only after the worktree read or bootstrap has
	// fully succeeded: a degraded Linear authority never changes the resume
	// outcome, and a failed resume never spends a Linear call.
	output.LinearRemote = checkLinearRemote(ctx, s, input.WorkID)
	return writeJSON(out, output, errOut)
}
