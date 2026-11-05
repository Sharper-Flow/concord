package store

import (
	"context"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

type neighborOperation struct {
	name    string
	prepare func(*testing.T) (*Store, func() error, func())
}

// Each row prepares before the neighbor takes its snapshot/lock, executes the
// real owning entry point, and checks committed authority after acknowledgement.
func TestDurableFamiliesWithNeighbor(t *testing.T) {
	for _, mode := range []neighbor.Mode{neighbor.Reader, neighbor.Writer} {
		for _, row := range durableNeighborOperations() {
			t.Run(string(mode)+"/"+row.name, func(t *testing.T) {
				s, run, verify := row.prepare(t)
				if mode == neighbor.Reader {
					// A TRUNCATE regression must fail fast rather than pin this
					// suite for the production timeout on every reader row.
					if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
						t.Fatal(err)
					}
				}
				neighbor.Start(t, s.Path(), mode)
				if err := run(); err != nil {
					t.Fatalf("%s under %s neighbor: %v", row.name, mode, err)
				}
				verify()
			})
		}
	}
}

func durableNeighborOperations() []neighborOperation {
	ctx := context.Background()
	rows := []neighborOperation{}
	for _, name := range []string{"RegisterTrustedClient", "UpdateTrustedClientPolicy", "MutateTrustedClientPolicy", "RotateTrustedClientKey", "RevokeTrustedClient", "persist_grant"} {
		rows = append(rows, neighborOperation{name, func(t *testing.T) (*Store, func() error, func()) {
			s := openTemp(t)
			client := TrustedClientRecord{ClientRef: "neighbor-client", Status: "active", PrincipalRef: "neighbor-principal", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}
			key := TrustedClientKeyRecord{ClientRef: client.ClientRef, KeyID: "neighbor-key", PublicKey: make([]byte, 32), Status: "active"}
			now := "2026-08-09T00:00:00Z"
			if name != "RegisterTrustedClient" {
				if err := s.RegisterTrustedClient(ctx, client, key, now); err != nil {
					t.Fatal(err)
				}
			}
			run := func() error {
				switch name {
				case "RegisterTrustedClient":
					return s.RegisterTrustedClient(ctx, client, key, now)
				case "UpdateTrustedClientPolicy":
					client.CapabilitiesJSON = `["product_read","work_define"]`
					return s.UpdateTrustedClientPolicy(ctx, client.ClientRef, client, now)
				case "MutateTrustedClientPolicy":
					return s.MutateTrustedClientPolicy(ctx, client.ClientRef, func(c TrustedClientRecord) (TrustedClientRecord, error) {
						c.CapabilitiesJSON = `["product_read","work_define"]`
						return c, nil
					})
				case "RotateTrustedClientKey":
					key.KeyID = "rotated-key"
					key.PublicKey[0] = 1
					return s.RotateTrustedClientKey(ctx, client.ClientRef, key, now)
				case "RevokeTrustedClient":
					return s.RevokeTrustedClient(ctx, client.ClientRef, now)
				default:
					return s.TransactDurable(ctx, func(tx *Transaction) error {
						return mutateTrustedClientPolicyTx(ctx, tx, client.ClientRef, func(c TrustedClientRecord) (TrustedClientRecord, error) {
							c.CapabilitiesJSON = `["product_read","work_define"]`
							return c, nil
						})
					})
				}
			}
			verify := func() {
				var status, capabilities, keyID string
				if err := s.db.QueryRow(`SELECT status,capabilities_json FROM agent_clients WHERE client_ref=?`, client.ClientRef).Scan(&status, &capabilities); err != nil {
					t.Fatal(err)
				}
				wantStatus := "active"
				if name == "RevokeTrustedClient" {
					wantStatus = "revoked"
				}
				if status != wantStatus {
					t.Fatalf("status=%s want %s", status, wantStatus)
				}
				if name == "persist_grant" || name == "MutateTrustedClientPolicy" || name == "UpdateTrustedClientPolicy" {
					if capabilities != `["product_read","work_define"]` {
						t.Fatalf("capabilities=%s", capabilities)
					}
				}
				if name == "RotateTrustedClientKey" {
					if err := s.db.QueryRow(`SELECT key_id FROM agent_client_keys WHERE client_ref=? AND status='active'`, client.ClientRef).Scan(&keyID); err != nil {
						t.Fatal(err)
					}
					if keyID != "rotated-key" {
						t.Fatalf("key=%s", keyID)
					}
				}
			}
			return s, run, verify
		}})
	}
	rows = append(rows,
		neighborOperation{"AuthorizeWorkflowActionAtBoundary", func(t *testing.T) (*Store, func() error, func()) {
			s := openTemp(t)
			actor, version := continuityTestWorkflow(t, s, "neighbor-work")
			version = issue31WorkflowActionWithPayload(t, s, "neighbor-work", version, "record_plan", "neighbor-plan", actor, []byte(testWorkflowPlanDocument))
			payload := []byte(`{"step_id":"plan","note":"The committed plan step survives."}`)
			run := func() error {
				return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, WorkflowActionPreflightRequest{WorkID: "neighbor-work", ExpectedVersion: version, ActionID: "complete_step", Payload: payload, Actor: actor}, nil, func(tx *Transaction) error {
					_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{WorkID: "neighbor-work", ExpectedVersion: version, ActionID: "complete_step", Payload: payload, Actor: actor, AcceptedInputsDigest: testDigest("neighbor"), IdempotencyIdentity: "neighbor-proposal", OperationID: "neighbor-proposal", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "neighbor-proposal", RequestID: "neighbor-request", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)})
					return err
				})
			}
			return s, run, func() {
				if got := workflowPlanFixtureFrontier(t, s, "neighbor-work"); got != "implement" {
					t.Fatalf("step=%s", got)
				}
			}
		}},
		neighborOperation{"workflow completion", func(t *testing.T) (*Store, func() error, func()) {
			s, event := seedCompletionGateCase(t, "neighbor-complete", completionGateCase{includeSpec: true, includePremise: true})
			return s, func() error { return CompleteWorkflow(ctx, s, event) }, func() {
				var state string
				if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='neighbor-complete'`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state != "completed" {
					t.Fatalf("state=%s", state)
				}
			}
		}},
	)
	return rows
}
