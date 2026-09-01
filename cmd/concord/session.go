package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	selectedProductEnv = "CONCORD_SELECTED_PRODUCT_ID"
	selectedWorkEnv    = "CONCORD_SELECTED_WORK_ID"
)

var sessionIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$`)

type sessionBootstrapFunc func(context.Context, string, string, string) ([]byte, error)
type sessionRunnerFunc func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error
type sessionAgentIdentityFunc func() error

// sessionDirectoryFunc resolves the directory the session runs in from the
// selected work's Project (CD-0093 D1). It is a parameter so tests can point
// a session at an isolated directory; production wiring is
// resolveSessionDirectory below.
type sessionDirectoryFunc func(ctx context.Context, workID string) (string, error)

// sessionDirectoryUnresolvedError reports a recorded canonical path that does
// not resolve on this machine. CD-0093 D3 refuses the launch rather than
// starting a session in a fallback directory, and the diagnostic names the
// path so the operator can see which registration is stale.
type sessionDirectoryUnresolvedError struct {
	Path  string
	Cause error
}

func (e *sessionDirectoryUnresolvedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("session directory does not resolve: %s; cause: %v", e.Path, e.Cause)
	}
	return fmt.Sprintf("session directory does not resolve: %s", e.Path)
}

// resolveSessionDirectory is the production wiring for CD-0093 D1 and D3: it
// reads the selected work's primary Project canonical path from the authority
// store and verifies the path resolves on this machine. Every failure refuses;
// no session starts in a fallback directory.
func resolveSessionDirectory(ctx context.Context, workID string) (string, error) {
	path, err := databasePath()
	if err != nil {
		return "", err
	}
	s, err := store.Open(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	directory, err := s.ProjectDirectoryForWork(ctx, workID)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", &sessionDirectoryUnresolvedError{Path: directory, Cause: err}
	}
	if !info.IsDir() {
		return "", &sessionDirectoryUnresolvedError{Path: directory, Cause: fmt.Errorf("recorded canonical path is not a directory")}
	}
	return directory, nil
}

// sessionOrchestratorFunc verifies the orchestrator identity the session
// requires and records the assertion as a domain event. CD-0061 D4 and D5
// bind the two steps: the verification proves the host has the required
// definition; the record is the evidence Concord later has if anyone asks
// what the session asserted. Both run inside the session command so a
// launcher-started session either has the recorded event or refuses with a
// typed absence diagnostic. On success it returns the invocation handle the
// host registers the resolved definition under; the session must select that
// name when it starts the host, so the agent that runs is the agent the
// assertion recorded.
//
// The function is a parameter so tests can inject an isolated temp store;
// production wiring is hostOrchestratorIdentity below.
type sessionOrchestratorFunc func(ctx context.Context, productID, workID string) (string, error)

// hostLaneAgentIdentity asserts the registered lanes against the host the
// session will start on.
func hostLaneAgentIdentity() error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return verifyLaneAgentIdentity(os.Getenv("HOME"), cwd, store.BuiltinLaneDefinitions())
}

// hostOrchestratorIdentity is the production wiring for the session's
// orchestrator assertion. It runs the file/digest verification against
// HOME/cwd, opens the authority store, and records the assertion in a single
// transaction. The verification runs before any store interaction so a
// missing definition fails closed without touching the database — the
// session either records the assertion it required or refuses.
func hostOrchestratorIdentity(ctx context.Context, productID, workID string) (handleResult string, errResult error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	assertion, handle, err := verifyOrchestratorIdentity(os.Getenv("HOME"), cwd)
	if err != nil {
		return "", err
	}
	// A resolvable definition is not proof the host will start it under the
	// derived handle. Establish that before the store is touched, so a
	// session that cannot run as the agent it asserts records nothing and
	// refuses (issue #430).
	if err := verifyHostRegistersHandle(ctx, probeHostAgentRegistry, cwd, handle); err != nil {
		return "", err
	}
	assertion.ProductID = productID
	assertion.WorkID = workID
	assertion.PrincipalRef = "principal/orchestrator"
	assertion.ClientRef = "client/concord-session"
	assertion.AgentRef = "agent/" + orchestratorAgentFileName
	assertion.SessionRef = "session/" + productID
	path, err := databasePath()
	if err != nil {
		return "", err
	}
	s, err := store.Open(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil && errResult == nil {
			handleResult = ""
			errResult = closeErr
		}
	}()
	eventID := orchestratorAssertionEventID(productID, workID)
	if _, err := s.RecordOrchestratorIdentityAssertion(ctx, eventID, s.Now(), assertion); err != nil {
		return "", err
	}
	return handle, nil
}

// orchestratorAssertionEventID returns the durable event_id Concord assigns
// to the orchestrator assertion for a session. It incorporates productID
// (always) and workID (when present) so concurrent sessions on the same
// authority do not collide, and a monotonic nanosecond counter so back-to-
// back sessions on the same scope record distinct events.
func orchestratorAssertionEventID(productID, workID string) string {
	scope := productID
	if workID != "" {
		scope = workID
	}
	return "session-orchestrator-identity:" + scope + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// DeriveSessionBoot reads the canonical continuity projection and renders the
// deterministic session boot packet shared by session transports.
func DeriveSessionBoot(ctx context.Context, database, productID, workID string) (packet []byte, errResult error) {
	s, err := store.Open(ctx, database)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil && errResult == nil {
			packet = nil
			errResult = closeErr
		}
	}()
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: workID, Limit: 1})
	if err != nil {
		return nil, err
	}
	return sessionboot.Build(productID, snapshot)
}

func runContinuityBlockCommand(args []string, out, errOut io.Writer) int {
	return runContinuityBlockCommandWithBootstrap(args, out, errOut, DeriveSessionBoot)
}

func runContinuityBlockCommandWithBootstrap(args []string, out, errOut io.Writer, bootstrap sessionBootstrapFunc) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, "concord continuity-block: unsupported arguments")
		return 2
	}
	productID, workID := os.Getenv(selectedProductEnv), os.Getenv(selectedWorkEnv)
	if workID == "" {
		return 0
	}
	if !sessionIdentity.MatchString(productID) || !sessionIdentity.MatchString(workID) {
		writeDiagnostic(errOut, "concord continuity-block: launcher identity is missing or invalid")
		return 2
	}
	database, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	packet, err := bootstrap(context.Background(), database, productID, workID)
	if err != nil {
		writeDiagnostic(errOut, "concord continuity-block: "+err.Error())
		return 1
	}
	if _, err := out.Write(packet); err != nil {
		writeDiagnostic(errOut, "concord continuity-block: "+err.Error())
		return 1
	}
	return 0
}

func runOpenCode(ctx context.Context, dir string, argv, env []string, in io.Reader, out, errOut io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the sole caller builds fixed opencode argv values and this call does not invoke a shell.
	// CD-0093 D1: the child's working directory is the session directory
	// resolved from the selected work's Project. The interactive host carries
	// no --dir flag; setting the process directory covers the project root
	// and every relative path the session resolves.
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}

// runSessionCommand is the entry point for `concord session`. The four
// injected callbacks (directory, bootstrap, runner, identity) make the
// command observable for tests; the orchestrator callback adds the durable
// orchestrator assertion CD-0061 D4 requires. Production wiring is
// resolveSessionDirectory, hostLaneAgentIdentity and
// hostOrchestratorIdentity.
func runSessionCommand(args []string, in io.Reader, out, errOut io.Writer, terminal bool,
	directory sessionDirectoryFunc, bootstrap sessionBootstrapFunc, runner sessionRunnerFunc,
	identity sessionAgentIdentityFunc, orchestrator sessionOrchestratorFunc) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, "concord session: unsupported arguments")
		return 2
	}
	if !terminal {
		writeDiagnostic(errOut, "concord session requires an interactive TTY")
		return 2
	}
	productID, workID := os.Getenv(selectedProductEnv), os.Getenv(selectedWorkEnv)
	if !sessionIdentity.MatchString(productID) || (workID != "" && !sessionIdentity.MatchString(workID)) {
		writeDiagnostic(errOut, "concord session: launcher identity is missing or invalid")
		return 2
	}
	// CD-0093 D1 and D3: the session directory resolves from the selected
	// work's Project before anything else the session does, and a session
	// with no selected work has no owning Project, so no directory can be
	// established. D3 refuses rather than starting a session in the
	// launcher's inherited directory.
	if workID == "" {
		writeDiagnostic(errOut, "concord session: no selected work; the session directory resolves from the selected work's Project")
		return 2
	}
	sessionDir, err := directory(context.Background(), workID)
	if err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	if err := identity(); err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	handle, err := orchestrator(context.Background(), productID, workID)
	if err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	if handle == "" {
		// The verification inside the orchestrator callback resolves a
		// definition or refuses; an empty handle means the resolved
		// definition registered under no name, so the host cannot be told
		// which agent to run. CD-0049 D4 admits no degraded start.
		writeDiagnostic(errOut, "concord session: orchestrator definition resolved without an invocation handle")
		return 2
	}
	path, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	packet, err := bootstrap(context.Background(), path, productID, workID)
	if err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 1
	}
	prompt := "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet)
	// The session starts the host as the agent whose identity it just
	// asserted and recorded, selecting it by the handle the host registers
	// the resolved definition under. Omitting the selection — or selecting
	// any other string — records evidence for an agent that never ran,
	// because the host answers an unselected name with the operator's
	// default agent and exits zero (CD-0049 D2).
	argv := []string{"opencode", "--agent", handle, "--prompt", prompt}
	if err := runner(context.Background(), sessionDir, argv, os.Environ(), in, out, errOut); err != nil {
		writeDiagnostic(errOut, fmt.Sprintf("concord session: opencode: %v", err))
		return 1
	}
	return 0
}
