package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func workerControlProofFixture(t *testing.T, caps []Capability) (*Service, WorkerControlAssertion, ed25519.PrivateKey) {
	t.Helper()
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service := NewService(db)
	service.Now = fixedTime
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{ClientRef: "client-1", KeyID: "key-control", PublicKey: pub, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: caps, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}, AgentScope: []string{"agent-1"}}}); err != nil {
		t.Fatal(err)
	}
	a := WorkerControlAssertion{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "child-session", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, WorkID: "work-control", ProductID: "product-1", ProjectID: "project-1", AttemptID: "attempt-control", PacketDigest: "sha256:" + strings.Repeat("a", 64), WorktreeIdentity: "sha256:" + strings.Repeat("b", 64), ControlID: "control:fixture", LeaseID: "lease-control", RequestID: "request-control", IssuedAt: fixedTime().Format(time.RFC3339Nano), Nonce: "control-proof-nonce"}
	a.Signature = ed25519.Sign(key, CanonicalWorkerControlAssertion(a))
	return service, a, key
}

func checkWorkerControlProof(t *testing.T, service *Service, a WorkerControlAssertion) (Authority, error) {
	t.Helper()
	var result Authority
	err := service.Store.Transact(context.Background(), func(tx *store.Transaction) error {
		var err error
		result, err = service.verifyWorkerControlAssertionTx(context.Background(), tx, probedHost(), a)
		return err
	})
	return result, err
}

func TestWorkerControlProofRequiresActualAgentExecutionPolicy(t *testing.T) {
	service, a, key := workerControlProofFixture(t, []Capability{"work_transition"})
	result, err := checkWorkerControlProof(t, service, a)
	if err != nil || result.AgentRef != a.AgentRef || result.SessionRef != a.SessionRef || result.PrincipalRef != "human-1" {
		t.Fatalf("signed actual child was not authorized: %+v %v", result, err)
	}
	a.AgentRef = "ungranted-child"
	a.Signature = ed25519.Sign(key, CanonicalWorkerControlAssertion(a))
	if _, err := checkWorkerControlProof(t, service, a); err == nil || !strings.Contains(err.Error(), "agent scope") {
		t.Fatalf("signed ungranted child borrowed coordinator authority: %v", err)
	}
}

func TestWorkerControlCanonicalSharedHostVector(t *testing.T) {
	raw, err := os.ReadFile("../../adapter/opencode/worker-control-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Assertion WorkerControlAssertion `json:"assertion"`
		Canonical string                 `json:"canonical"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if got := string(CanonicalWorkerControlAssertion(vector.Assertion)); got != vector.Canonical {
		t.Fatalf("host and Go worker control framing differ: %q", got)
	}
}

func TestWorkerControlProofNeverUsesEvidenceOnlyCapability(t *testing.T) {
	service, a, _ := workerControlProofFixture(t, []Capability{CapabilityWorkerEvidence})
	if _, err := checkWorkerControlProof(t, service, a); err == nil || !strings.Contains(err.Error(), "capability outside trusted client policy") {
		t.Fatalf("evidence-only client acquired execution authority: %v", err)
	}
}

func TestWorkerControlProofSignsEveryIdentityField(t *testing.T) {
	service, original, _ := workerControlProofFixture(t, []Capability{"work_transition"})
	value := reflect.ValueOf(original)
	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).Kind() != reflect.String {
			continue
		}
		t.Run(value.Type().Field(i).Name, func(t *testing.T) {
			changed := original
			field := reflect.ValueOf(&changed).Elem().Field(i)
			field.SetString(field.String() + "-changed")
			if _, err := checkWorkerControlProof(t, service, changed); err == nil {
				t.Fatal("changed signed field was accepted")
			}
		})
	}
}

func TestWorkerControlProofRefusesRevokedClient(t *testing.T) {
	service, a, _ := workerControlProofFixture(t, []Capability{"work_transition"})
	if err := service.RevokeClient(context.Background(), a.ClientRef); err != nil {
		t.Fatal(err)
	}
	if _, err := checkWorkerControlProof(t, service, a); err == nil {
		t.Fatal("revoked client retained worker execution authority")
	}
}

func TestWorkerControlScopeRejectsForeignWorkBeforeProducerBinding(t *testing.T) {
	service, a, key := workerControlProofFixture(t, []Capability{"work_transition"})
	events := agentFixtureProductEvents("foreign-control", "product-foreign", "project-foreign", "Foreign", "Foreign Project", "control scope fixture")
	events = append(events,
		store.Event{EventID: "foreign-control-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "foreign-control-work", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Foreign control","priority":1}`)},
		store.Event{EventID: "foreign-control-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "foreign-control-work", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-foreign","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	)
	if err := store.ApplyOperation(context.Background(), service.Store, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-foreign"): 0, store.VersionRef(store.SubjectProject, "project-foreign"): 0, store.VersionRef(store.SubjectWorkItem, "foreign-control-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	a.WorkID = "foreign-control-work"
	a.Signature = ed25519.Sign(key, CanonicalWorkerControlAssertion(a))
	req := store.WorkerControlRequest{WorkID: a.WorkID, ProjectID: a.ProjectID, AttemptID: a.AttemptID, WorktreeIdentity: a.WorktreeIdentity, ControlID: a.ControlID, Packet: json.RawMessage(`{"work_id":"foreign-control-work"}`)}
	err := service.Store.Transact(context.Background(), func(tx *store.Transaction) error {
		_, _, err := service.resolveAuthorizedWorkerControlTx(context.Background(), tx, probedHost(), req, a)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "work reference is outside authorized Product scope") {
		t.Fatalf("foreign work reached native dispatch lookup instead of Product authorization: %v", err)
	}
}

func TestWorkerControlScopeUsesSignedHostProductWithoutInventingPacketFields(t *testing.T) {
	service, a, _ := workerControlProofFixture(t, []Capability{"work_transition"})
	events := []store.Event{
		{EventID: "control-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: a.WorkID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Control","priority":1}`)},
		{EventID: "control-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: a.WorkID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), service.Store, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, a.WorkID): 0}}); err != nil {
		t.Fatal(err)
	}
	req := store.WorkerControlRequest{WorkID: a.WorkID, ProjectID: a.ProjectID, AttemptID: a.AttemptID, WorktreeIdentity: a.WorktreeIdentity, ControlID: a.ControlID, Packet: json.RawMessage(`{"work_id":"work-control","attempt_id":"attempt-control"}`)}
	err := service.Store.Transact(context.Background(), func(tx *store.Transaction) error {
		_, _, err := service.resolveAuthorizedWorkerControlTx(context.Background(), tx, probedHost(), req, a)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no authorized dispatch window exists") {
		t.Fatalf("signed host Product did not reach dispatch validation with the published packet shape: %v", err)
	}
}
