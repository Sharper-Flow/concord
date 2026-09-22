package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// runRecoverFoldGuardCommand is the offline recovery route for a database
// ordinary Open refuses because a fold_guard row committed without a closing
// scope. It is separate from concord repair (#912): repair re-installs the
// release over the network, while this verb needs nothing but the database
// file, so it routes around the store open like the release verbs.
func runRecoverFoldGuardCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var request struct {
		Path string `json:"path"`
	}
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(append([]string{"recover-fold-guard"}, args...), " ")))
		writeUsage(errOut)
		return 2
	}
	inputLimit := int64(agent.MaxEnvelopeBytes)
	raw, err := io.ReadAll(io.LimitReader(in, inputLimit+1))
	if err != nil || int64(len(raw)) > inputLimit {
		writeDiagnostic(errOut, fmt.Sprintf("input exceeds %d bytes", inputLimit))
		return 1
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeOperatorDiagnostic(errOut, "recover-fold-guard", err.Error())
		return 1
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeOperatorDiagnostic(errOut, "recover-fold-guard", "trailing JSON")
		return 1
	}
	path := request.Path
	if path == "" {
		path, err = databasePath()
		if err != nil {
			writeOperatorDiagnostic(errOut, "recover-fold-guard", err.Error())
			return 1
		}
	}
	report, err := store.RecoverFoldGuard(context.Background(), path)
	if err != nil {
		writeOperatorDiagnostic(errOut, "recover-fold-guard", err.Error())
		return 1
	}
	_, _ = fmt.Fprintf(out, "recover-fold-guard: cleared the stranded guard; rebuilt %d event(s) of projections at %s\n", report.Events, path)
	return 0
}
