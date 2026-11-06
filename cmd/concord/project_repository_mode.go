package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

func runProjectRepositoryModeUpdate(raw []byte, s *store.Store, out, errOut io.Writer) int {
	const command = "project-repository-mode-update"
	var request struct {
		ProjectID       string `json:"project_id"`
		RepositoryMode  string `json:"repository_mode"`
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	ctx := context.Background()
	result, err := s.SetProjectRepositoryMode(ctx, store.ProjectRepositoryModeChange{ProjectID: request.ProjectID, RepositoryMode: request.RepositoryMode, ExpectedVersion: request.ExpectedVersion, Reason: request.Reason})
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
}
