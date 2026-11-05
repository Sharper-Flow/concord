package main

import (
	"context"
	"fmt"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

// runCDReservations reads reservations and their holders from core-owned authority
// data. Unreleased claims in the store may not be visible in peer Git refs.
// The unauthenticated read relies on filesystem access to the authority database;
// it appends no event and must not become a second write authority.
func runCDReservations(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		Directory string `json:"directory"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", diagnosticText(err))
		return 1
	}
	if request.Directory == "" {
		writeOperatorDiagnostic(errOut, "cd-reservations", "directory is required")
		return 1
	}
	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, request.Directory, request.Directory)
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", diagnosticText(err))
		return 1
	}
	_, productIDs, err := s.ScopeVersion(ctx, resolution.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", diagnosticText(err))
		return 1
	}
	if len(productIDs) != 1 {
		writeOperatorDiagnostic(errOut, "cd-reservations", fmt.Sprintf("the calling checkout resolves to %d Products; designate one Product knowledge home so the reservation owner is unambiguous", len(productIDs)))
		return 1
	}
	reservations, err := s.LawAdditionReservations(ctx, productIDs[0])
	if err != nil {
		writeOperatorDiagnostic(errOut, "cd-reservations", diagnosticText(err))
		return 1
	}
	return writeJSON(out, map[string]any{
		"schema_version": "concord.cd-reservations.v1",
		"product_id":     productIDs[0],
		"project_id":     resolution.ProjectID,
		"reservations":   reservations,
	}, errOut)
}
