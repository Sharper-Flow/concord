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
type sessionRunnerFunc func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error
type sessionAgentIdentityFunc func() error

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
func hostOrchestratorIdentity(ctx context.Context, productID, workID string) (string, error) {
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
	defer s.Close()
	eventID := orchestratorAssertionEventID(productID, workID)
	if _, err := s.RecordOrchestratorIdentityAssertion(ctx, eventID, time.Now().UTC(), assertion); err != nil {
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

func deriveSessionBoot(ctx context.Context, database, productID, workID string) ([]byte, error) {
	s, err := store.Open(ctx, database)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: workID, Limit: 1})
	if err != nil {
		return nil, err
	}
	return sessionboot.Build(productID, snapshot)
}

func runOpenCode(ctx context.Context, argv, env []string, in io.Reader, out, errOut io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}

// runSessionCommand is the entry point for `concord session`. The three
// injected callbacks (bootstrap, runner, identity) make the command
// observable for tests; the orchestrator callback adds the durable
// orchestrator assertion CD-0061 D4 requires. Production wiring is
// hostLaneAgentIdentity and hostOrchestratorIdentity.
func runSessionCommand(args []string, in io.Reader, out, errOut io.Writer, terminal bool,
	bootstrap sessionBootstrapFunc, runner sessionRunnerFunc,
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
	prompt := "Concord identity: product_id=" + productID
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
		prompt = "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet)
	}
	// The session starts the host as the agent whose identity it just
	// asserted and recorded, selecting it by the handle the host registers
	// the resolved definition under. Omitting the selection — or selecting
	// any other string — records evidence for an agent that never ran,
	// because the host answers an unselected name with the operator's
	// default agent and exits zero (CD-0049 D2).
	argv := []string{"opencode", "--agent", handle, "--prompt", prompt}
	if err := runner(context.Background(), argv, os.Environ(), in, out, errOut); err != nil {
		writeDiagnostic(errOut, fmt.Sprintf("concord session: opencode: %v", err))
		return 1
	}
	return 0
}
