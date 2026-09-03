package main

import (
	"context"
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
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
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
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.ProjectID != input.ProjectID || !resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "work-bootstrap", "invocation must be the requested Project main checkout")
		return 1
	}
	result, err := s.BootstrapWorktree(context.Background(), store.BootstrapRequest{
		ProductID: input.ProductID, ProjectID: input.ProjectID, Title: input.Title,
		ValueStatement: input.ValueStatement, Kind: input.Kind, Task: input.Task,
		IdempotencyKey: input.IdempotencyKey, Priority: input.Priority, Urgency: input.Urgency,
		Tags: input.Tags, WorkflowTypeRef: input.WorkflowTypeRef, ExternalRef: input.ExternalRef,
		GoverningRequirements: input.GoverningRequirements, Ref: input.Ref,
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
}

type sessionPrepareOutput struct {
	SchemaVersion string `json:"schema_version"`
	Agent         string `json:"agent"`
	Directory     string `json:"directory"`
	ProductID     string `json:"product_id"`
	WorkID        string `json:"work_id"`
	Prompt        string `json:"prompt"`
}

var sessionPrepareID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// runSessionPrepare verifies that the current directory is the claimed
// worktree, resolves the agent and lane identity that directory defines, and
// derives the session boot packet. It records nothing: the session's worktree
// is the directory it runs in, and the host owns that fact.
func runSessionPrepare(raw []byte, s *store.Store, out, errOut io.Writer, laneIdentity sessionAgentIdentityFunc, identity sessionOrchestratorFunc, bootstrap sessionBootstrapFunc) int {
	var input sessionPrepareInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || input.Task == "" || len(input.Task) > 8192 || strings.ContainsRune(input.Task, '\x00') || !utf8.ValidString(input.Task) {
		writeOperatorDiagnostic(errOut, "session-prepare", "product_id, work_id, and bounded task are required")
		return 1
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
	var entry store.WorktreeEntry
	for _, candidate := range entries {
		if candidate.State == "active" {
			if entry.ProjectID != "" {
				writeOperatorDiagnostic(errOut, "session-prepare", "work item has more than one active worktree")
				return 1
			}
			entry = candidate
		}
	}
	if entry.ProjectID == "" {
		writeOperatorDiagnostic(errOut, "session-prepare", "work item has no active verified worktree")
		return 1
	}
	if !samePath(cwd, entry.Path) {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory is not the claimed worktree")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil || resolution.ProjectID != entry.ProjectID || resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "session-prepare", "current directory does not resolve to the claimed Project worktree")
		return 1
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
		return 1
	}
	_, products, err := s.ScopeVersion(context.Background(), entry.ProjectID)
	if err != nil || len(products) != 1 || products[0] != input.ProductID {
		writeOperatorDiagnostic(errOut, "session-prepare", "claimed Project is not in the requested Product scope")
		return 1
	}
	// cwd is the claimed worktree this command verified above. The identity
	// callbacks receive it as their directory: the definitions and registry
	// they verify are the ones that directory resolves (CD-0093 D2).
	if err := laneIdentity(cwd); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
	}
	handle, err := identity(context.Background(), cwd, input.ProductID, input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", err.Error())
		return 1
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
	prompt := "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet) + "\nTask: " + input.Task
	if len(prompt) > agent.MaxEnvelopeBytes {
		writeOperatorDiagnostic(errOut, "session-prepare", "launch prompt exceeds 65536 bytes")
		return 1
	}
	return writeJSON(out, sessionPrepareOutput{SchemaVersion: "1.0", Agent: handle, Directory: cwd, ProductID: input.ProductID, WorkID: input.WorkID, Prompt: prompt}, errOut)
}

func samePath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftResolved) == filepath.Clean(rightResolved)
}
