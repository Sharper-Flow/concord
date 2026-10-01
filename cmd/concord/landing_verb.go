package main

import (
	"context"
	"io"
	"time"
)

// landingVerbInput is the request shape both adapter-invoked landing verbs
// decode. Neither verb is an agent tool operation, and no agent names its
// inputs.
type landingVerbInput struct {
	WorkID          string `json:"work_id"`
	SessionRef      string `json:"session_ref"`
	LandedDirectory string `json:"landed_directory"`
	HostPID         int    `json:"host_pid"`
}

// runLandingVerb decodes, validates, and records one adapter-invoked landing.
// The record call is the verb's whole behavior; every refusal records
// nothing and writes one operator diagnostic.
func runLandingVerb(raw []byte, out, errOut io.Writer, verb string, record func(context.Context, landingVerbInput, time.Time) (any, error)) int {
	var request landingVerbInput
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, verb, err.Error())
		return 1
	}
	if request.WorkID == "" || request.SessionRef == "" || request.LandedDirectory == "" || request.HostPID <= 0 {
		writeOperatorDiagnostic(errOut, verb, "work_id, session_ref, landed_directory, and a positive host_pid are required")
		return 1
	}
	landing, err := record(context.Background(), request, time.Now().UTC())
	if err != nil {
		writeOperatorDiagnostic(errOut, verb, err.Error())
		return 1
	}
	return writeJSON(out, landing, errOut)
}
