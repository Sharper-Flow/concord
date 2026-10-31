package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// seedReservationAuthority registers a Product, Project, membership, two work
// items, and a canonical_path locator, then folds one reserved law id per
// work, so cd-reservations has authority and
// projection state to read. The reservation FKs reach the work contracts and
// their architecture bindings, so the fixture seeds the full chain the fold
// writes.
func seedReservationAuthority(t *testing.T, s *store.Store, repo string) {
	t.Helper()
	seedLocatorAuthority(t, s, repo)
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "cd-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-other", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 4, Payload: jsonRaw(`{"work_kind":"task","title":"Other Reservation Work","priority":1}`)},
		{EventID: "cd-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-other", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-wl","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-other"): 0}}))
	actorRef := store.DeriveWorkflowActorRef("principal/reservations", "client/reservations", "agent/reservations", "session/reservations")
	ownerDigest := "sha256:" + strings.Repeat("a", 64)
	otherDigest := "sha256:" + strings.Repeat("b", 64)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	step := func(label string, statement string, arguments ...any) {
		t.Helper()
		if _, err := db.Exec(statement, arguments...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	step("actors", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'operator','now')`, actorRef, "principal/reservations", "client/reservations", "agent/reservations", "session/reservations")
	step("contracts", `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES
			('work-wl',1,'reservation fixture','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal'),
			('work-other',1,'reservation fixture','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal')`, actorRef, actorRef)
	step("bindings", `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES
			('work-wl',1,'product-wl',?,'root',?),
			('work-other',1,'product-wl',?,'root',?)`, ownerDigest, ownerDigest, otherDigest, otherDigest)
	step("reservations", `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES
			('product-wl','CD-0189','work-wl',1,'root'),
			('product-wl','CD-0190','work-other',1,'root')`)
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// Host placement does not assign work ownership to a checkout.
func TestCDReservationsListsProductReservationsFromHostWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedReservationAuthority(t, s, repo)

	claimedRoot := filepath.Join(repo, "worktrees", "work-wl")
	run := func(arguments ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, out)
		}
	}
	run("worktree", "add", "-b", "work/work-wl", claimedRoot)

	claimed := claimedRoot
	var out, errOut bytes.Buffer
	if code := runCDReservations([]byte(`{"directory":"`+claimed+`"}`), s, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var got struct {
		SchemaVersion string `json:"schema_version"`
		ProductID     string `json:"product_id"`
		ProjectID     string `json:"project_id"`
		Reservations  []struct {
			LawID       string `json:"law_id"`
			OwnerWorkID string `json:"owner_work_id"`
		} `json:"reservations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.SchemaVersion != "concord.cd-reservations.v1" {
		t.Errorf("schema_version=%q", got.SchemaVersion)
	}
	if got.ProductID != "product-wl" || got.ProjectID != "project-wl" {
		t.Errorf("product=%q project=%q, want product-wl/project-wl", got.ProductID, got.ProjectID)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["checkout_work_id"]; present {
		t.Error("reservation read assigns work ownership to the host checkout")
	}
	if len(got.Reservations) != 2 || got.Reservations[0].LawID != "CD-0189" || got.Reservations[0].OwnerWorkID != "work-wl" || got.Reservations[1].LawID != "CD-0190" || got.Reservations[1].OwnerWorkID != "work-other" {
		t.Errorf("reservations=%+v, want CD-0189/work-wl then CD-0190/work-other in law-id order", got.Reservations)
	}
}

func TestCDReservationsListsMainCheckoutReservations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedReservationAuthority(t, s, repo)

	var out, errOut bytes.Buffer
	if code := runCDReservations([]byte(`{"directory":"`+repo+`"}`), s, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var got struct {
		Reservations []struct {
			LawID string `json:"law_id"`
		} `json:"reservations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if len(got.Reservations) != 2 {
		t.Errorf("reservations=%+v, want both product reservations", got.Reservations)
	}
}

// The verb reads. It must never become a second write authority (CD-0021).
func TestCDReservationsWritesNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedReservationAuthority(t, s, repo)
	before := durableCounts(t, s)

	var out, errOut bytes.Buffer
	if code := runCDReservations([]byte(`{"directory":"`+repo+`"}`), s, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if after := durableCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("cd-reservations changed durable state: before=%v after=%v", before, after)
	}
}

func TestCDReservationsRequiresDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedReservationAuthority(t, s, repo)
	for _, raw := range []string{`{}`, `{"directory":""}`} {
		var out, errOut bytes.Buffer
		if code := runCDReservations([]byte(raw), s, &out, &errOut); code == 0 {
			t.Errorf("%s resolved without a directory: %q", raw, out.String())
		}
	}
}
