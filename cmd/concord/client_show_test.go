package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func seedClientShow(t *testing.T, capabilities string) (*store.Store, ed25519.PrivateKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, path)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	client := store.TrustedClientRecord{ClientRef: "client-show", Status: "active", PrincipalRef: "principal-show", CapabilitiesJSON: capabilities, ProductScopeJSON: `["product-show"]`, ProjectScopeJSON: `["project-show"]`, AgentScopeJSON: `["agent-show"]`}
	key := store.TrustedClientKeyRecord{ClientRef: client.ClientRef, KeyID: "key-show", PublicKey: private.Public().(ed25519.PublicKey), Status: "active"}
	if err := s.RegisterTrustedClient(context.Background(), client, key, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return s, private
}

func TestClientShowPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, capabilities, status string
		revoke                     bool
	}{
		{"read-only", `["product_read"]`, "active", false},
		{"write-capability", `["product_read","work_define"]`, "active", false},
		{"revoked", `["product_read"]`, "revoked", true},
		{"empty-capabilities", `[]`, "active", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, private := seedClientShow(t, tc.capabilities)
			if tc.revoke {
				if err := s.RevokeTrustedClient(context.Background(), "client-show", "2026-01-02T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			before := durableCounts(t, s)
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"client-show"}, strings.NewReader(`{"client_ref":"client-show"}`), &out, &errOut); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr=%q", errOut.String())
			}
			wantJSON := `{"client_ref":"client-show","status":"` + tc.status + `","principal_ref":"principal-show","capabilities":` + tc.capabilities + `,"product_scope":["product-show"],"project_scope":["project-show"],"agent_scope":["agent-show"],"policy_revision":1}`
			var got, want map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s, want %s", out.String(), wantJSON)
			}
			for _, forbidden := range []string{"key-show", base64.StdEncoding.EncodeToString(private), base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey))} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("output contains key material: %s", out.String())
				}
			}
			if after := durableCounts(t, s); !reflect.DeepEqual(before, after) {
				t.Fatalf("read changed durable counts: %v -> %v", before, after)
			}
		})
	}
}

func TestClientShowUnknownClient(t *testing.T) {
	seedClientShow(t, `["product_read"]`)
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"client", "show"}, strings.NewReader(`{"client_ref":"missing"}`), &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "projection_not_found") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestClientShowInputRefusals(t *testing.T) {
	seedClientShow(t, `["product_read"]`)
	for _, tc := range []struct {
		input string
		code  int
	}{
		{`{}`, 64}, {`{"client_ref":""}`, 1}, {`{"client_ref":null}`, 1},
		{`{"client_ref":7}`, 1}, {`{"client_ref":"client-show","extra":true}`, 1},
		{`{"client_ref":"client-show"} {}`, 1}, {`{"client_ref":`, 1},
		{`{"client_ref":"` + strings.Repeat("a", 129) + `"}`, 1},
		{`{"client_ref":"` + strings.Repeat("é", 129) + `"}`, 1},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"client-show"}, strings.NewReader(tc.input), &out, &errOut); code != tc.code || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, tc.code, out.String(), errOut.String())
			}
		})
	}
}

func TestClientShowPolicyUpdateRotationAndEmptyScopes(t *testing.T) {
	s, _ := seedClientShow(t, `["product_read"]`)
	ctx := context.Background()
	policy := store.TrustedClientRecord{PrincipalRef: "principal-updated", CapabilitiesJSON: `["work_define"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}
	if err := s.UpdateTrustedClientPolicy(ctx, "client-show", policy, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	key := store.TrustedClientKeyRecord{ClientRef: "client-show", KeyID: "key-rotated", Status: "active", PublicKey: bytes.Repeat([]byte{9}, 32)}
	if err := s.RotateTrustedClientKey(ctx, "client-show", key, "2026-01-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"client", "show"}, strings.NewReader(`{"client_ref":"client-show"}`), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	want := `{"client_ref":"client-show","status":"active","principal_ref":"principal-updated","capabilities":["work_define"],"product_scope":[],"project_scope":[],"agent_scope":[],"policy_revision":2}` + "\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestClientShowAcceptsRegisteredMultibyteReferences(t *testing.T) {
	for _, ref := range []string{strings.Repeat("é", 65), strings.Repeat("é", 128), "client\x00" + strings.Repeat("é", 129)} {
		t.Run(ref, func(t *testing.T) {
			seedClientShow(t, `["product_read"]`)
			private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
			registration, err := json.Marshal(map[string]any{
				"client_ref": ref, "key_id": "multibyte-key", "principal_ref": "principal-multibyte",
				"public_key":   base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)),
				"capabilities": []string{"product_read"}, "product_scope": []string{}, "project_scope": []string{}, "agent_scope": []string{},
			})
			if err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"client-register"}, bytes.NewReader(registration), &out, &errOut); code != 0 {
				t.Fatalf("registration exit=%d stderr=%q", code, errOut.String())
			}
			request, err := json.Marshal(map[string]string{"client_ref": ref})
			if err != nil {
				t.Fatal(err)
			}
			out.Reset()
			errOut.Reset()
			if code := runWithInput([]string{"client-show"}, bytes.NewReader(request), &out, &errOut); code != 0 {
				t.Fatalf("inspection exit=%d stderr=%q", code, errOut.String())
			}
			var response struct {
				ClientRef string `json:"client_ref"`
			}
			if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.ClientRef != ref {
				t.Fatalf("response=%q err=%v", out.String(), err)
			}
		})
	}
}

func TestClientShowMalformedStoredPolicy(t *testing.T) {
	for _, tc := range []struct {
		column, policy string
	}{
		{"capabilities_json", `[null]`},
		{"product_scope_json", `[7]`},
		{"project_scope_json", `[{}]`},
		{"agent_scope_json", `[null]`},
	} {
		t.Run(tc.column+tc.policy, func(t *testing.T) {
			s, _ := seedClientShow(t, `["product_read"]`)
			if _, err := s.DatabaseForTesting().Exec(`UPDATE agent_clients SET `+tc.column+`=? WHERE client_ref='client-show'`, tc.policy); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"client-show"}, strings.NewReader(`{"client_ref":"client-show"}`), &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "invalid stored client policy") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestClientShowPolicyStringArray(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `"policy"`, `7`, `broken`, `[null]`, `[7]`, `[{}]`} {
		if values, err := clientPolicyStrings(raw); err == nil {
			t.Errorf("accepted policy %s as %v", raw, values)
		}
	}
	for _, raw := range []string{`[]`, `["product_read"]`, `["product-a","product-b"]`} {
		values, err := clientPolicyStrings(raw)
		if err != nil || values == nil {
			t.Errorf("policy %s decoded as %v: %v", raw, values, err)
		}
	}
}

func TestClientShowDoesNotChangePolicyOrKeys(t *testing.T) {
	s, _ := seedClientShow(t, `["product_read"]`)
	beforeClient, beforeKey, err := s.TrustedClientWithKey(context.Background(), "client-show")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"client-show"}, strings.NewReader(`{"client_ref":"client-show"}`), &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	afterClient, afterKey, err := s.TrustedClientWithKey(context.Background(), "client-show")
	if err != nil || beforeClient != afterClient || !reflect.DeepEqual(beforeKey, afterKey) {
		t.Fatalf("read changed policy or keys: client=%+v key=%+v err=%v", afterClient, afterKey, err)
	}
}
