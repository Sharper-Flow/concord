package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/store"
)

type clientShowResponse struct {
	ClientRef      string   `json:"client_ref"`
	Status         string   `json:"status"`
	PrincipalRef   string   `json:"principal_ref"`
	Capabilities   []string `json:"capabilities"`
	ProductScope   []string `json:"product_scope"`
	ProjectScope   []string `json:"project_scope"`
	AgentScope     []string `json:"agent_scope"`
	PolicyRevision int64    `json:"policy_revision"`
}

func runClientShow(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		ClientRef string `json:"client_ref"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "client-show", err.Error())
		return 1
	}
	// Match registration's SQLite length(TEXT) constraint, which stops at NUL.
	referenceText, _, _ := strings.Cut(request.ClientRef, "\x00")
	if length := utf8.RuneCountInString(referenceText); length < 1 || length > 128 {
		writeOperatorDiagnostic(errOut, "client-show", "client_ref must contain 1-128 Unicode characters before the first NUL")
		return 1
	}
	client, err := s.TrustedClient(context.Background(), request.ClientRef)
	if err != nil {
		writeOperatorDiagnostic(errOut, "client-show", err.Error())
		return 1
	}
	response := clientShowResponse{ClientRef: client.ClientRef, Status: client.Status, PrincipalRef: client.PrincipalRef, PolicyRevision: client.PolicyRevision}
	for _, policy := range []struct {
		raw    string
		target *[]string
	}{
		{client.CapabilitiesJSON, &response.Capabilities},
		{client.ProductScopeJSON, &response.ProductScope},
		{client.ProjectScopeJSON, &response.ProjectScope},
		{client.AgentScopeJSON, &response.AgentScope},
	} {
		values, err := clientPolicyStrings(policy.raw)
		if err != nil {
			writeOperatorDiagnostic(errOut, "client-show", "invalid stored client policy")
			return 1
		}
		*policy.target = values
	}
	return writeJSON(out, response, errOut)
}

func clientPolicyStrings(raw string) ([]string, error) {
	var values []*string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, fmt.Errorf("policy must be a string array")
	}
	result := make([]string, len(values))
	for i, value := range values {
		if value == nil {
			return nil, fmt.Errorf("policy must not contain null")
		}
		result[i] = *value
	}
	return result, nil
}
