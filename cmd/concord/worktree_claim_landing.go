package main

import (
	"context"
	"io"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// runWorktreeClaimLanding records a verified claim landing. The verb is
// adapter-invoked, like host-lease: it is not an agent tool operation, and no
// agent names its inputs. The OpenCode adapter calls it only after the host
// read the session's directory back as the claimed path, so the landing this
// verb records is evidence, not intent. In one transaction the core confirms
// the destination row is active and occupied by the calling session, clears
// that session's occupancy on its other active rows of the same work item,
// and appends one durable event naming the session, work item, source paths,
// landed path, and the host process identity. The adapter sends the pid of
// the OpenCode process that holds the session; the core reads that process's
// start time from /proc itself and never accepts one from the caller.
func runWorktreeClaimLanding(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		WorkID          string `json:"work_id"`
		SessionRef      string `json:"session_ref"`
		LandedDirectory string `json:"landed_directory"`
		HostPID         int    `json:"host_pid"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "claim-landing", err.Error())
		return 1
	}
	if request.WorkID == "" || request.SessionRef == "" || request.LandedDirectory == "" || request.HostPID <= 0 {
		writeOperatorDiagnostic(errOut, "claim-landing", "work_id, session_ref, landed_directory, and a positive host_pid are required")
		return 1
	}
	landing, err := s.RecordWorktreeClaimLanding(context.Background(), store.WorktreeClaimLandingRequest{
		WorkID:          request.WorkID,
		SessionRef:      request.SessionRef,
		LandedDirectory: request.LandedDirectory,
		HostPID:         request.HostPID,
		Now:             time.Now().UTC(),
	})
	if err != nil {
		writeOperatorDiagnostic(errOut, "claim-landing", err.Error())
		return 1
	}
	return writeJSON(out, landing, errOut)
}
