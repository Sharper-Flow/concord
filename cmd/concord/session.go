package main

import (
	"context"
	"os"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

// sessionBootstrapFunc derives the deterministic session boot packet for one
// Product, work, and resolved Project. Tests inject an isolated derivation for session-prepare.
type sessionBootstrapFunc func(ctx context.Context, database, productID, workID, projectID string, fits func([]byte) (bool, error)) ([]byte, error)

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

// DeriveSessionBoot reads the stored resume plan and handoff frontier and renders the
// deterministic session boot packet shared by session transports.
func DeriveSessionBoot(ctx context.Context, database, productID, workID, projectID string, fits func([]byte) (bool, error)) (packet []byte, errResult error) {
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
	snapshot, err := store.ReadSessionResume(ctx, s, workID, projectID)
	if err != nil {
		return nil, err
	}
	return sessionboot.Build(productID, projectID, snapshot, fits)
}
