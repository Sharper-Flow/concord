package main

import (
	"context"
	"io"
	"os"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

// sessionBootstrapFunc derives the deterministic session boot packet for one
// Product and work. session-prepare and the continuity-block transport share
// it; tests inject an isolated derivation.
type sessionBootstrapFunc func(context.Context, string, string, string) ([]byte, error)

// sessionOrchestratorFunc verifies the selected agent against its definition
// and the calling host's registry in dir. It performs no store write.
type sessionOrchestratorFunc func(ctx context.Context, dir string, host hostCommandResolution, agent string) (string, error)

func hostOrchestratorIdentity(_ context.Context, dir string, host hostCommandResolution, agent string) (string, error) {
	handle, err := verifyOrchestratorIdentity(os.Getenv("HOME"), dir, agent)
	if err != nil {
		return "", err
	}
	// A resolvable definition is not proof the host registers its handle.
	if err := verifyHostRegistersHandle(host, handle); err != nil {
		return "", err
	}
	return handle, nil
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
