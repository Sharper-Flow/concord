package main

import (
	"context"
	"fmt"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

// runCDReservations answers "which CD law ids does this checkout's Product
// hold reserved, and for whom": the folded law-addition reservations an agent
// must treat as taken when it allocates the next CD id or checks a branch's
// records. Git alone cannot answer this — a reservation lives in the store
// from contract approval until the reservation is released, so an unmerged
// branch's claim is invisible to every peer-ref comparison.
//
// Placement: a core CLI verb rather than a host script, the same rationale
// project-resolve carries — the reservation table and the worktree ownership
// are authority data only the core can read. The verb is unauthenticated, as
// project-resolve and project-canonical-path are (CD-0079 D2): the trust
// boundary is filesystem access to the authority database, which a caller
// able to exec this verb already holds. This is a read. It appends no event,
// and CD-0021's rejection of a second write authority still binds.
//
// checkout_work_id names the work item whose active claimed worktree holds
// the calling directory, or "" for a main checkout. A consumer that needs to
// separate "a reservation of mine the branch dropped" from "a reservation
// another work holds" joins on it.
func runCDReservations(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		Directory string `json:"directory"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", err.Error())
		return 1
	}
	if request.Directory == "" {
		writeOperatorDiagnostic(errOut, "cd-reservations", "directory is required")
		return 1
	}
	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, request.Directory, request.Directory)
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", err.Error())
		return 1
	}
	_, productIDs, err := s.ScopeVersion(ctx, resolution.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", err.Error())
		return 1
	}
	if len(productIDs) != 1 {
		writeOperatorDiagnostic(errOut, "cd-reservations", fmt.Sprintf("the calling checkout resolves to %d Products; designate one Product knowledge home so the reservation owner is unambiguous", len(productIDs)))
		return 1
	}
	reservations, err := s.LawAdditionReservations(ctx, productIDs[0])
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", err.Error())
		return 1
	}
	owner, err := s.WorktreeOwnerWorkID(ctx, resolution.ProjectID, resolution.Repository.WorktreePath)
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", err.Error())
		return 1
	}
	return writeJSON(out, map[string]any{
		"schema_version":   "concord.cd-reservations.v1",
		"product_id":       productIDs[0],
		"project_id":       resolution.ProjectID,
		"checkout_work_id": owner,
		"reservations":     reservations,
	}, errOut)
}
