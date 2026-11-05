package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

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

const sessionPrepareRefusalExit = 2
const sessionPreparePromptPrefix = "Concord session boot packet (core-derived at its source-version watermark; reread concord_work_trace.resume and item-scoped concord_work_browse.list detail=full before consequential action. Reconstruct referenced documents and verify digest/length before use: hash compact UTF-8 JSON in source member order with Go JSON escaping for <, >, &, U+2028 and U+2029; preserve fragment bytes without re-encoding. If changed or unavailable, re-prepare boot):\n"

// Untyped failures keep the caller's ordinary failure or identity-refusal status.
func sessionPrepareFailureExit(err error, untypedExit int) int {
	var failure *store.Failure
	if errors.As(err, &failure) {
		if failure.RetrySafe {
			return 1
		}
		return sessionPrepareRefusalExit
	}
	return untypedExit
}

// Identity is verified before the boot packet is derived.
// The registry comes from one bare probe in the session's directory.
func runSessionPrepare(raw []byte, s *store.Store, out, errOut io.Writer, hostCommand sessionHostCommandFunc, identity sessionOrchestratorFunc, bootstrap sessionBootstrapFunc) int {
	var input sessionPrepareInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareRefusalExit
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.WorkID) || !sessionPrepareID.MatchString(input.Agent) || len(input.Task) > 8192 || strings.ContainsRune(input.Task, '\x00') || !utf8.ValidString(input.Task) {
		writeOperatorDiagnostic(errOut, "session-prepare", "product_id, work_id, and agent are required, and task must be bounded valid UTF-8")
		return sessionPrepareRefusalExit
	}
	if err := s.RequireActionableWork(context.Background(), input.WorkID, "session-prepare"); err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, sessionPrepareRefusalExit)
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", "cannot read current directory")
		return 1
	}
	resolution, err := s.ResolveProject(context.Background(), cwd, cwd)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, 1)
	}
	if resolution.MainWorktree {
		writeOperatorDiagnostic(errOut, "session-prepare", "session preparation requires a host-created Project worktree, not its main checkout")
		return sessionPrepareRefusalExit
	}
	workProjects, err := s.ProjectsForWork(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, 1)
	}
	projectMember := false
	for _, project := range workProjects {
		if project.ID == resolution.ProjectID {
			projectMember = true
			break
		}
	}
	if !projectMember {
		writeOperatorDiagnostic(errOut, "session-prepare", "resolved Project is not a work membership")
		return sessionPrepareRefusalExit
	}
	_, products, err := s.ScopeVersion(context.Background(), resolution.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, 1)
	}
	if len(products) != 1 || products[0] != input.ProductID {
		writeOperatorDiagnostic(errOut, "session-prepare", "resolved Project is not in the requested Product scope")
		return sessionPrepareRefusalExit
	}
	host, err := hostCommand(context.Background(), cwd)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		var interrupted *hostProbeInterruptedError
		if errors.As(err, &interrupted) {
			return 1
		}
		return sessionPrepareRefusalExit
	}
	handle, err := identity(context.Background(), cwd, host, input.Agent)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, sessionPrepareRefusalExit)
	}
	database, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return 1
	}
	summary, err := s.ReadWorkItemSummary(context.Background(), input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, 1)
	}
	taskTail := ""
	if input.Task != "" {
		taskTail = "\nTask: " + input.Task
	}
	render := func(packet []byte) sessionPrepareOutput {
		return sessionPrepareOutput{SchemaVersion: "1.0", Agent: handle, Directory: cwd, ProductID: input.ProductID, WorkID: input.WorkID, Title: summary.Title, Prompt: sessionPreparePromptPrefix + string(packet) + taskTail}
	}
	fits := func(packet []byte) (bool, error) {
		value := render(packet)
		data, err := json.Marshal(value)
		if err != nil {
			return false, err
		}
		return len(value.Prompt) <= agent.MaxEnvelopeBytes && len(data) <= agent.MaxEnvelopeBytes, nil
	}
	packet, err := bootstrap(context.Background(), database, input.ProductID, input.WorkID, resolution.ProjectID, fits)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return sessionPrepareFailureExit(err, 1)
	}
	fit, err := fits(packet)
	if err != nil {
		writeOperatorDiagnostic(errOut, "session-prepare", diagnosticText(err))
		return 1
	}
	if !fit {
		writeOperatorDiagnostic(errOut, "session-prepare", "launch prompt exceeds 65536 bytes")
		return sessionPrepareRefusalExit
	}
	return writeJSON(out, render(packet), errOut)
}
