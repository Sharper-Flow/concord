package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

// sessionBootstrapFunc derives the deterministic session boot packet for one
// Product and work. session-prepare and the continuity-block transport share
// it; tests inject an isolated derivation.
type sessionBootstrapFunc func(context.Context, string, string, string) ([]byte, error)

// sessionOrchestratorFunc verifies the identity of the active agent in dir
// and records the assertion as a domain event. CD-0061 D4 binds the two
// steps: the verification proves the host has the required definition; the
// record is the evidence Concord later has if anyone asks what the session
// asserted.
//
// host is the registry resolution the caller made in dir: the registry the
// verification checks is the document the probe carried back, so the verified
// registry is the one the calling host resolves.
//
// agent is the active host agent the session runs as: the request's agent
// field for session-prepare. The host answers an unselected name with the
// operator's default agent and exits zero (CD-0049 D2), so the verification
// proves the named agent resolves, registers, and is the one the session
// selected.
//
// The function is a parameter so tests can inject an isolated temp store;
// production wiring is hostOrchestratorIdentity below.
type sessionOrchestratorFunc func(ctx context.Context, dir string, host hostCommandResolution, productID, workID, agent string) (string, error)

// hostOrchestratorIdentity is the production wiring for the session's
// orchestrator assertion. It runs the file/digest verification for the active
// agent in the caller's directory, checks the registry document the probe
// carried back, opens the authority store, and records the assertion in a
// single transaction. The verification runs before any store interaction so a
// missing definition fails closed without touching the database — the session
// either records the assertion it required or refuses.
func hostOrchestratorIdentity(ctx context.Context, dir string, host hostCommandResolution, productID, workID, agent string) (string, error) {
	return recordOrchestratorIdentity(ctx, os.Getenv("HOME"), host, dir, productID, workID, agent)
}

// recordOrchestratorIdentity carries the whole assertion. The home directory
// is a parameter so a test drives the real path against a temporary
// installation instead of restating it. host is the resolution the caller
// made in dir: the registry document the probe resolved is what the
// registration check reads.
func recordOrchestratorIdentity(ctx context.Context, home string, host hostCommandResolution, dir, productID, workID, agent string) (handleResult string, errResult error) {
	assertion, handle, err := verifyOrchestratorIdentity(home, dir, agent)
	if err != nil {
		return "", err
	}
	// A resolvable definition is not proof the host will start it under the
	// derived handle. Establish that before the store is touched, so a
	// session that cannot run as the agent it asserts records nothing and
	// refuses. The registry probed is the one the calling host
	// resolves in dir, the directory the session itself runs in.
	if err := verifyHostRegistersHandle(host, handle); err != nil {
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
	s, err := openStoreForCommand(ctx, path)
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
	s, err := openStoreForCommand(ctx, database)
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

// DeriveSessionBootForDirectory resolves the session directory through the
// claimed worktree to the active work item and renders the deterministic
// session boot packet for that item. A directory that resolves no active
// claim returns no packet and no error: the caller renders no block rather
// than the launch item's.
func DeriveSessionBootForDirectory(ctx context.Context, database, directory string) (packet []byte, errResult error) {
	s, err := openStoreForCommand(ctx, database)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil && errResult == nil {
			packet = nil
			errResult = closeErr
		}
	}()
	resolution, err := s.ResolveContinuityWorkByDirectory(ctx, directory)
	if err != nil {
		return nil, err
	}
	if resolution.WorkID == "" {
		return nil, nil
	}
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: resolution.WorkID, Limit: 1})
	if err != nil {
		return nil, err
	}
	return sessionboot.Build(resolution.ProductID, snapshot)
}

// continuityDirectoryFunc derives the continuity packet for the work item a
// session directory resolves to. It is a parameter so tests can inject an
// isolated derivation; production wiring is DeriveSessionBootForDirectory.
type continuityDirectoryFunc func(context.Context, string, string) ([]byte, error)

func runContinuityBlockCommand(args []string, out, errOut io.Writer) int {
	return runContinuityBlockCommandWithDirectory(args, out, errOut, DeriveSessionBootForDirectory)
}

// runContinuityBlockCommandWithDirectory prints the continuity packet for the
// work item the one supplied session directory resolves to. A directory that
// resolves no active claim prints nothing and exits 0, so the caller renders
// no block rather than the launch item's.
func runContinuityBlockCommandWithDirectory(args []string, out, errOut io.Writer, bootstrap continuityDirectoryFunc) int {
	if len(args) != 1 {
		writeDiagnostic(errOut, "concord continuity-block: supply exactly one session directory")
		return 2
	}
	database, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return 1
	}
	packet, err := bootstrap(context.Background(), database, args[0])
	if err != nil {
		writeDiagnostic(errOut, "concord continuity-block: "+diagnosticText(err))
		return 1
	}
	if len(packet) == 0 {
		return 0
	}
	if _, err := out.Write(packet); err != nil {
		writeDiagnostic(errOut, "concord continuity-block: "+diagnosticText(err))
		return 1
	}
	return 0
}
