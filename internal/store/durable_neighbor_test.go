package store

import (
	"context"
	"fmt"
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
						return MutateTrustedClientPolicyTx(ctx, tx, client.ClientRef, func(c TrustedClientRecord) (TrustedClientRecord, error) {
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
			payload := []byte(`{"problem":"A bounded problem.","affected":["The store."],"stakes":"The committed proposal survives.","user_outcomes":["A durable proposal."]}`)
			run := func() error {
				return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: "neighbor-work", ExpectedVersion: version, ActionID: "record_proposal", Payload: payload, Actor: actor}, nil, time.Time{}, nil, func(tx *Transaction) error {
					_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: "neighbor-work", ExpectedVersion: version, ActionID: "record_proposal", Payload: payload, Actor: actor, AcceptedInputsDigest: testDigest("neighbor"), IdempotencyIdentity: "neighbor-proposal", OperationID: "neighbor-proposal", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "neighbor-proposal", RequestID: "neighbor-request", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)})
					return err
				})
			}
			return s, run, func() {
				if got := currentStep(t, s, "neighbor-work"); got != "alignment" {
					t.Fatalf("step=%s", got)
				}
			}
		}},
		neighborOperation{"CompleteWorkflowWithRegistry", func(t *testing.T) (*Store, func() error, func()) {
			s, event := seedCompletionGateCase(t, "neighbor-complete", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
			return s, func() error { return CompleteWorkflowWithRegistry(ctx, s, BuiltinWorkflowRegistry(), event) }, func() {
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
	for _, name := range []string{"ClaimStepAuthorized", "CompleteStep"} {
		rows = append(rows, neighborOperation{name, func(t *testing.T) (*Store, func() error, func()) {
			s := openTemp(t)
			claim := testClaim("neighbor-fence", "neighbor-claim")
			var epoch int64
			if name == "CompleteStep" {
				claimed, err := ClaimStep(ctx, s, claim)
				if err != nil {
					t.Fatal(err)
				}
				epoch = claimed.AttemptEpoch
			}
			called := false
			run := func() error {
				if name == "ClaimStepAuthorized" {
					_, err := ClaimStepAuthorized(ctx, s, claim, func(tx *Transaction) error {
						called = true
						var count int
						return tx.tx.QueryRowContext(ctx, "SELECT count(*) FROM domain_events").Scan(&count)
					})
					return err
				}
				_, err := CompleteStep(ctx, s, completionRequest(claim.OpID, epoch, "neighbor-complete", `{"ok":true}`))
				return err
			}
			return s, run, func() {
				result, err := Step(ctx, s, claim.OpID)
				if err != nil {
					t.Fatal(err)
				}
				if result.AttemptEpoch != 1 {
					t.Fatalf("epoch=%d", result.AttemptEpoch)
				}
				if name == "ClaimStepAuthorized" && !called {
					t.Fatal("authorization not called")
				}
				if name == "CompleteStep" && result.ResultKind != ResultCompleted {
					t.Fatalf("result=%s", result.ResultKind)
				}
			}
		}})
	}
	rows = append(rows, neighborOperation{"ClaimWorktree", func(t *testing.T) (*Store, func() error, func()) {
		s, git, _ := worktreeFixture(t)
		return s, func() error { _, err := s.ClaimWorktree(ctx, baseClaim(git)); return err }, func() {
			if got := countRows(t, s, "worktree_entries"); got != 1 {
				t.Fatalf("entries=%d", got)
			}
		}
	}})
	rows = append(rows, neighborOperation{"RecoverFoldGuard", func(t *testing.T) (*Store, func() error, func()) {
		s := seedQueryFixture(t)
		before := countRows(t, s, "domain_events")
		if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		return s, func() error {
				report, err := RecoverFoldGuard(ctx, s.Path())
				if err == nil && (!report.Rebuilt || report.Events != int64(before)) {
					return fmt.Errorf("recovery report=%+v want events=%d", report, before)
				}
				return err
			}, func() {
				assertFoldGuardEmpty(t, s)
				if got := countRows(t, s, "domain_events"); got != before {
					t.Fatalf("events=%d want %d", got, before)
				}
			}
	}})
	for _, name := range []string{"bootstrap_journal", "bootstrap_rollback", "cross_project_bootstrap"} {
		rows = append(rows, neighborOperation{name, func(t *testing.T) (*Store, func() error, func()) {
			s := openTemp(t)
			repo := initBootstrapStoreRepo(t)
			seedBootstrapStoreAuthority(t, s, repo)
			var result BootstrapResult
			if name == "cross_project_bootstrap" {
				var err error
				result, err = s.BootstrapWorktree(ctx, bootstrapStoreRequest(), nil)
				if err != nil {
					t.Fatal(err)
				}
				seedBootstrapProject(t, s, "project-neighbor", initBootstrapStoreRepo(t))
				replaceBootstrapMemberships(t, s, result.WorkID, readWorkVersion(t, s, result.WorkID), "project-neighbor")
			}
			run := func() error {
				if name == "cross_project_bootstrap" {
					_, err := s.BootstrapExistingWorktree(ctx, ExistingBootstrapRequest{ProductID: "product-bootstrap", ProjectID: "project-neighbor", WorkID: result.WorkID}, nil)
					return err
				}
				var err error
				result, err = s.BootstrapWorktree(ctx, bootstrapStoreRequest(), nil)
				if err != nil {
					return err
				}
				if name == "bootstrap_rollback" {
					location := WorktreeLocation{Repo: repo, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA, Path: result.Entry.Path}
					return s.rollbackBootstrap(ctx, result.OperationID, result.WorkID, location, ExecGitRunner{}, fmt.Errorf("synthetic rollback"))
				}
				return nil
			}
			return s, run, func() {
				var state string
				if err := s.db.QueryRow(`SELECT state FROM bootstrap_operations LIMIT 1`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				want := "completed"
				if name == "bootstrap_rollback" {
					want = "rolled_back"
				}
				if state != want {
					t.Fatalf("journal=%s want %s", state, want)
				}
			}
		}})
	}
	return rows
}
