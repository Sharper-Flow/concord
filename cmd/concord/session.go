package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	selectedProductEnv = "CONCORD_SELECTED_PRODUCT_ID"
	selectedWorkEnv    = "CONCORD_SELECTED_WORK_ID"
	selectedPromptEnv  = "CONCORD_SELECTED_PROMPT"
	selectedProjectEnv = "CONCORD_SELECTED_PROJECT_PATH"
)

var sessionIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$`)

type sessionBootstrapFunc func(context.Context, string, string, string) ([]byte, error)

// sessionDirectoryFunc resolves the directory the session runs in from the
// selected work's Project (CD-0093 D1). It is a parameter so tests can
// inject an isolated resolution; production wiring is hostSessionDirectory
// below.
type sessionDirectoryFunc func(ctx context.Context, workID string) (string, error)

// sessionRunnerFunc starts the host in dir. The directory is a parameter so
// the executor it stands in for observes the same resolved directory the
// identity and registry steps used (CD-0093 D2).
type sessionRunnerFunc func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error

// sessionAgentIdentityFunc asserts the registered lanes against the host the
// session will start on, in the resolved session directory.
type sessionAgentIdentityFunc func(dir string) error

// sessionOrchestratorFunc verifies the orchestrator identity the session
// requires in dir and records the assertion as a domain event. CD-0061 D4 and D5
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
type sessionOrchestratorFunc func(ctx context.Context, dir, productID, workID string) (string, error)

// sessionDirectoryUnresolvedError reports a canonical path that does not
// resolve to a directory on this machine. CD-0093 D3 admits no fallback
// directory, so the diagnostic names the path that failed to resolve.
type sessionDirectoryUnresolvedError struct {
	Path  string
	Cause error
}

func (e *sessionDirectoryUnresolvedError) Error() string {
	message := fmt.Sprintf("resolved Project directory is not a usable directory: %s", e.Path)
	if e.Cause != nil {
		message += fmt.Sprintf("; cause: %v", e.Cause)
	}
	return message
}

func (e *sessionDirectoryUnresolvedError) Unwrap() error { return e.Cause }

// hostSessionDirectory is the production wiring for the session's directory
// resolution. It opens the authority store, resolves the canonical path of
// the primary Project that owns the selected work, and requires that path
// to resolve to a directory on this machine. Every failure is typed and
// refuses the launch (CD-0093 D3).
func hostSessionDirectory(ctx context.Context, workID string) (dirResult string, errResult error) {
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
			dirResult = ""
			errResult = closeErr
		}
	}()
	dir, err := s.ResolveSessionDirectory(ctx, workID)
	if err != nil {
		return "", err
	}
	// CD-0093 D3: a canonical path that is not absolute and clean resolves
	// differently depending on where the process started, which is the
	// ambiguity this decision removes. Refuse it rather than stat it.
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", &sessionDirectoryUnresolvedError{Path: dir}
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", &sessionDirectoryUnresolvedError{Path: dir, Cause: err}
	}
	if !info.IsDir() {
		return "", &sessionDirectoryUnresolvedError{Path: dir}
	}
	return dir, nil
}

// sessionDirectory resolves the one directory the session uses. Work-selected
// sessions take the owning Project's canonical path (CD-0093 D1). A
// Product-only session has no work-derived Project, so it keeps the launcher's
// directory; the value is still resolved once here, which is what CD-0093 D2
// requires of the identity, registry, and execution steps that follow.
func sessionDirectory(ctx context.Context, resolve sessionDirectoryFunc, workID string) (string, error) {
	if workID != "" {
		return resolve(ctx, workID)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the launcher directory for a Product-only session: %w", err)
	}
	return cwd, nil
}

// hostLaneAgentIdentity asserts the registered lanes against the host the
// session will start on, in the directory the session resolved. It reads no
// working directory of its own (CD-0093 D2).
func hostLaneAgentIdentity(dir string) error {
	return verifyLaneAgentIdentity(os.Getenv("HOME"), dir, store.BuiltinLaneDefinitions())
}

// hostOrchestratorIdentity is the production wiring for the session's
// orchestrator assertion. It runs the file/digest verification in the
// directory the session resolved, opens the authority store, and records
// the assertion in a single transaction. The verification runs before any
// store interaction so a missing definition fails closed without touching
// the database — the session either records the assertion it required or
// refuses.
func hostOrchestratorIdentity(ctx context.Context, dir, productID, workID string) (string, error) {
	return recordOrchestratorIdentity(ctx, os.Getenv("HOME"), probeHostAgentRegistry, dir, productID, workID)
}

// recordOrchestratorIdentity carries the whole assertion. The home directory
// and the registry probe are parameters so a test drives the real path against
// a temporary installation instead of restating it.
func recordOrchestratorIdentity(ctx context.Context, home string, probe hostRegistryProbeFunc, dir, productID, workID string) (handleResult string, errResult error) {
	assertion, handle, err := verifyOrchestratorIdentity(home, dir)
	if err != nil {
		return "", err
	}
	// A resolvable definition is not proof the host will start it under the
	// derived handle. Establish that before the store is touched, so a
	// session that cannot run as the agent it asserts records nothing and
	// refuses (issue #430). The registry probed is the one the host
	// resolves in dir, the directory the session itself runs in
	// (CD-0093 D2).
	if err := verifyHostRegistersHandle(ctx, probe, dir, handle); err != nil {
		return "", err
	}
	assertion.ProductID = productID
	assertion.WorkID = workID
	assertion.PrincipalRef = "principal/orchestrator"
	assertion.ClientRef = "client/concord-session"
	// The handle is the name the host registers the definition under, and the
	// name the session runs as. The file stem is neither once the definition
	// carries `name:` frontmatter, so recording it would attribute the session
	// to an agent that does not exist.
	assertion.AgentRef = "agent/" + handle
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
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}

// runSessionCommand is the entry point for `concord session`. The injected
// callbacks (directory, bootstrap, runner, identity, orchestrator) make the
// command observable for tests. Production wiring is hostSessionDirectory,
// DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, and
// hostOrchestratorIdentity. CD-0093 D2 fixes the order: the Project
// directory resolves before identity verification, never after, and the one
// resolved directory governs agent definition resolution, the host registry
// probe, and host execution. None of them reads the process working
// directory.
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
	projectPath := os.Getenv(selectedProjectEnv)
	if projectPath != "" {
		if productID != "" || workID != "" {
			writeDiagnostic(errOut, "concord session: project launch cannot carry Concord identity")
			return 2
		}
		if !filepath.IsAbs(projectPath) || filepath.Clean(projectPath) != projectPath {
			writeDiagnostic(errOut, "concord session: project path is not canonical: "+projectPath)
			return 2
		}
		// #nosec G703 -- projectPath passed the absolute and canonical
		// checks above, so the stat target is the validated path itself.
		info, statErr := os.Stat(projectPath)
		if statErr != nil || !info.IsDir() {
			writeDiagnostic(errOut, "concord session: project path is not a usable directory: "+projectPath)
			return 2
		}
		prompt := os.Getenv(selectedPromptEnv)
		if prompt == "" {
			prompt = "OpenCode project session"
		}
		argv := []string{"opencode", "--prompt", prompt}
		if err := runner(context.Background(), projectPath, argv, os.Environ(), in, out, errOut); err != nil {
			writeDiagnostic(errOut, fmt.Sprintf("concord session: opencode: %v", err))
			return 1
		}
		return 0
	}
	if !sessionIdentity.MatchString(productID) || (workID != "" && !sessionIdentity.MatchString(workID)) {
		writeDiagnostic(errOut, "concord session: launcher identity is missing or invalid")
		return 2
	}
	// CD-0093 D2: the session directory resolves once, before identity
	// verification, because a verification that runs first constrains the
	// wrong registry. Every later step takes this one value.
	//
	// With work selected, it is the canonical path of the Project that owns
	// that work (D1). A Product-only session selects no work, and a Product
	// spans Projects, so no work-derived Project exists to resolve; the
	// session keeps the directory the launcher was given. D2 binds the three
	// steps to one directory and is satisfied either way, because the value
	// is resolved once here rather than read separately by each step.
	dir, err := sessionDirectory(context.Background(), directory, workID)
	if err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	if err := identity(dir); err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	handle, err := orchestrator(context.Background(), dir, productID, workID)
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
	// A Product-only session carries identity and no continuity packet:
	// CD-0031 derives continuity from a selected work item, and there is
	// none to derive from.
	prompt := os.Getenv(selectedPromptEnv)
	if prompt == "" {
		prompt = "Concord identity: product_id=" + productID
	}
	if workID != "" {
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
		prompt = prompt + "\n\nConcord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet)
	}
	// The session starts the host as the agent whose identity it just
	// asserted and recorded, selecting it by the handle the host registers
	// the resolved definition under. Omitting the selection — or selecting
	// any other string — records evidence for an agent that never ran,
	// because the host answers an unselected name with the operator's
	// default agent and exits zero (CD-0049 D2). The child working
	// directory is the resolved Project directory: the host defaults its
	// project to it and every relative path the session resolves starts
	// there (CD-0093 D1).
	argv := []string{"opencode", "--agent", handle, "--prompt", prompt}
	if err := runner(context.Background(), dir, argv, os.Environ(), in, out, errOut); err != nil {
		writeDiagnostic(errOut, fmt.Sprintf("concord session: opencode: %v", err))
		return 1
	}
	return 0
}
