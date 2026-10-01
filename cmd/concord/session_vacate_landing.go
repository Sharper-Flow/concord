package main

import (
	"context"
	"io"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// runSessionVacateLanding records a verified vacate landing. The verb is
// adapter-invoked, like claim-landing: it is not an agent tool operation, and
// no agent names its inputs. The OpenCode adapter calls it only after the
// host moved the session and read its directory back as the registered main
// checkout the committed session vacate names, so the landing this verb
// records is evidence, not intent. In one transaction the core verifies the
// landed path against the committed relocation request and releases the
// calling session's occupancy rows, in any work item, while appending one
// durable event naming the session, work item, source paths, landed path, and
// the host process identity. The adapter sends the pid of the OpenCode
// process that holds the session; the core reads that process's start time
// from /proc itself and never accepts one from the caller. Every refusal
// records nothing, so a refused move, a destination mismatch, or an unreadable
// landing leaves occupancy standing.
func runSessionVacateLanding(raw []byte, s *store.Store, out, errOut io.Writer) int {
	return runLandingVerb(raw, s, out, errOut, "vacate-landing", func(ctx context.Context, request landingVerbInput, now time.Time) (any, error) {
		return s.RecordSessionVacateLanding(ctx, store.SessionVacateLandingRequest{
			WorkID:          request.WorkID,
			SessionRef:      request.SessionRef,
			LandedDirectory: request.LandedDirectory,
			HostPID:         request.HostPID,
			Now:             now,
		})
	})
}
