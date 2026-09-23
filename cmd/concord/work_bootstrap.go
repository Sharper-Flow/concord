package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

type workBootstrapInput struct {
	ProductID             string   `json:"product_id"`
	ProjectID             string   `json:"project_id"`
	Title                 string   `json:"title"`
	ValueStatement        string   `json:"value_statement"`
	Kind                  string   `json:"kind"`
	Task                  string   `json:"task"`
	IdempotencyKey        string   `json:"idempotency_key"`
	Priority              int64    `json:"priority"`
	Urgency               string   `json:"urgency"`
	Tags                  []string `json:"tags"`
	WorkflowTypeRef       string   `json:"workflow_type_ref"`
	ExternalRef           string   `json:"external_ref"`
	RaisedFromWorkID      string   `json:"raised_from_work_id"`
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
	SessionRef            string   `json:"session_ref"`
}

type workBootstrapOutput struct {
	SchemaVersion string            `json:"schema_version"`
	OperationID   string            `json:"operation_id"`
	Replayed      bool              `json:"replayed"`
	ProductID     string            `json:"product_id"`
	ProjectID     string            `json:"project_id"`
	WorkID        string            `json:"work_id"`
	WorkVersion   int64             `json:"work_version"`
	Worktree      workBootstrapTree `json:"worktree"`
}

type workBootstrapTree struct {
	SetID   string `json:"set_id"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`
	Path    string `json:"path"`
	State   string `json:"state"`
}

func runWorkBootstrap(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input workBootstrapInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", "cannot read invocation directory")
		return 1
	}
	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, cwd, cwd)
	if err != nil || resolution.ProjectID != input.ProjectID {
		writeOperatorDiagnostic(errOut, "work-bootstrap", "invocation must resolve to the requested Project")
		return 1
	}
	if !resolution.MainWorktree {
		if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
			writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
			return 1
		}
		if input.Ref == "" {
			input.Ref, err = store.DefaultBranchRef(ctx, resolution.Repository.CanonicalPath)
			if err != nil {
				writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
				return 1
			}
		}
	}
	result, err := s.BootstrapWorktree(ctx, store.BootstrapRequest{
		ProductID: input.ProductID, ProjectID: input.ProjectID, Title: input.Title,
		ValueStatement: input.ValueStatement, Kind: input.Kind, Task: input.Task,
		IdempotencyKey: input.IdempotencyKey, Priority: input.Priority, Urgency: input.Urgency,
		Tags: input.Tags, WorkflowTypeRef: input.WorkflowTypeRef, ExternalRef: input.ExternalRef, RaisedFromWorkID: input.RaisedFromWorkID,
		GoverningRequirements: input.GoverningRequirements, Ref: input.Ref, SessionRef: input.SessionRef,
	}, nil)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-bootstrap", err.Error())
		return 1
	}
	return writeJSON(out, workBootstrapOutput{
		SchemaVersion: "1.0", OperationID: result.OperationID, Replayed: result.Replayed,
		ProductID: result.ProductID, ProjectID: result.ProjectID, WorkID: result.WorkID,
		WorkVersion: result.WorkVersion,
		Worktree:    workBootstrapTree{SetID: result.Entry.SetID, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA, Path: result.Entry.Path, State: result.Entry.State},
	}, errOut)
}

type sessionPrepareInput struct {
	ProductID string `json:"product_id"`
	WorkID    string `json:"work_id"`
	Task      string `json:"task"`
	Agent     string `json:"agent"`
}

type sessionPrepareOutput struct {
	SchemaVersion string `json:"schema_version"`
	Agent         string `json:"agent"`
	Directory     string `json:"directory"`
	ProductID     string `json:"product_id"`
	WorkID        string `json:"work_id"`
	Title         string `json:"title"`
	Prompt        string `json:"prompt"`
}

var sessionPrepareID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// sessionPrepareRefusalExit is the typed exit status session-prepare reports
// for a deterministic refusal: invalid input, or a state or identity check
// that fails the same way until state changes — no active worktree at the
// current directory, Project resolution, work membership, Product scope,
// lane identity, orchestrator identity, prompt bound. Callers classify by
// this status alone, never by stderr text; the session-prepare commandSpecs
// entry declares it.
const sessionPrepareRefusalExit = 2

// sessionPrepareReadFailureExit classifies a store read failure. A typed
// failure the store marks unsafe to repeat is a refusal; every other failure
// may clear on a replay, so it keeps the ordinary failure status.
func sessionPrepareReadFailureExit(err error) int {
	var failure *store.Failure
	if errors.As(err, &failure) && !failure.RetrySafe {
		return sessionPrepareRefusalExit
	}
	return 1
}

// runSessionPrepare verifies that the current directory is an active claimed
// worktree of the work item — a multi-Project item holds one active worktree
// per Project, so the claimed entry is the active one whose path is this
// directory — then verifies the active host agent and the lane identity that
// directory defines, and derives the session boot packet. It records
// nothing: the session's worktree is the directory it runs in, and the host
// owns that fact.
func runSessionPrepare(raw []byte, s *store.Store, out, errOut io.Writer, laneIdentity sessionAgentIdentityFunc, identity sessionOrchestratorFunc, bootstrap sessionBootstrapFunc) int {
	var input sessionPrepareInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return sessionPrepareRefusalExit
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || !sessionPrepareID.MatchString(input.Agent) || len(input.Task) > 8192 || strings.ContainsRune(input.Task, '\x00') || !utf8.ValidString(input.Task) {
		writeOperatorDiagnostic(errOut, "session-prepare", "product_id, work_id, and agent are required, and task must be bounded valid UTF-8")
		return sessionPrepareRefusalExit
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", "cannot read current directory")
		return 1
	}
	entries, err := s.WorktreeEntries(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	// A multi-Project work item holds one active worktree per Project, so the
	// claimed entry is the active one whose path is this directory; another
	// Project's active entry is not a refusal.
	var entry store.WorktreeEntry
	matched := false
	for _, candidate := range entries {
		if candidate.State == "active" && samePath(cwd, candidate.Path) {
			entry, matched = candidate, true
			break
		}
	}
	if !matched {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory is not an active claimed worktree of this work item")
		return sessionPrepareRefusalExit
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return sessionPrepareReadFailureExit(err)
	}
	if resolution.ProjectID != entry.ProjectID || resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory does not resolve to the claimed Project worktree")
		return sessionPrepareRefusalExit
	}
	workProjects, err := s.ProjectsForWork(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	projectMember := false
	for _, project := range workProjects {
		if project.ID == entry.ProjectID {
			projectMember = true
			break
		}
	}
	if !projectMember {
		writeOperatorDiagnostic(errOut, "session-prepare", "claimed worktree Project is not a work membership")
		return sessionPrepareRefusalExit
	}
	_, products, err := s.ScopeVersion(context.Background(), entry.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return sessionPrepareReadFailureExit(err)
	}
	if len(products) != 1 || products[0] != input.ProductID {
		writeOperatorDiagnostic(errOut, "session-prepare", "claimed Project is not in the requested Product scope")
		return sessionPrepareRefusalExit
	}
	// cwd is the claimed worktree this command verified above. The identity
	// callbacks receive it as their directory: the definitions and registry
	// they verify are the ones that directory resolves (CD-0093 D2).
	if err := laneIdentity(cwd); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return sessionPrepareRefusalExit
	}
	handle, err := identity(context.Background(), cwd, input.ProductID, input.WorkID, input.Agent)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return sessionPrepareRefusalExit
	}
	database, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	packet, err := bootstrap(context.Background(), database, input.ProductID, input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	prompt := "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet)
	if input.Task != "" {
		prompt += "\nTask: " + input.Task
	}
	if len(prompt) > agent.MaxEnvelopeBytes {
		writeOperatorDiagnostic(errOut, "session-prepare", "launch prompt exceeds 65536 bytes")
		return sessionPrepareRefusalExit
	}
	// The work title rides the response so a successful work_start can name
	// the work in host surfaces (issue #917) on both the capture and resume
	// paths; the read records nothing.
	summary, err := s.ReadWorkItemSummary(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	return writeJSON(out, sessionPrepareOutput{SchemaVersion: "1.0", Agent: handle, Directory: cwd, ProductID: input.ProductID, WorkID: input.WorkID, Title: summary.Title, Prompt: prompt}, errOut)
}

func samePath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
}
