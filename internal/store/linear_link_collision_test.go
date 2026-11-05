package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// One live work item per Linear issue (CD-0146): the Linear identity rides
// linear_issue_links, capture writes the link row in the capture's
// transaction, and a live holder of the key or UUID refuses. A terminal
// holder never blocks a fresh capture.

func seedLinkWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	seedWork(t, s, workID)
}

func recordLink(t *testing.T, s *Store, workID, key, uuid string) {
	t.Helper()
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: workID, RemoteIssueUUID: uuid, HumanKey: key, URL: "https://linear.app/example/issue/" + key})
		return err
	}); err != nil {
		t.Fatalf("record link for %s: %v", workID, err)
	}
}

func TestLinearLinkOneLiveItemPerIssue(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedLinkWork(t, s, "link-first")
	recordLink(t, s, "link-first", "CON-8100", "uuid-link-1")

	// A second live item cannot take the same UUID or the same key.
	for _, probe := range []struct {
		name, key, uuid string
	}{
		{name: "same uuid", key: "CON-8101", uuid: "uuid-link-1"},
		{name: "same key", key: "CON-8100", uuid: "uuid-link-2"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			seedLinkWork(t, s, "link-second-"+probe.name)
			err := s.Transact(context.Background(), func(tx *Transaction) error {
				_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: "link-second-" + probe.name, RemoteIssueUUID: probe.uuid, HumanKey: probe.key, URL: "https://linear.app/example/issue/" + probe.key})
				return err
			})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidRelation {
				t.Fatalf("collision error = %v, want typed %s", err, KindInvalidRelation)
			}
			if !strings.Contains(failure.Detail, "another live work item") || strings.Contains(failure.Detail, "link-first") || failure.HoldingWorkID != "link-first" {
				t.Fatalf("refusal must carry the holder without publishing it: %+v", failure)
			}
			encoded, err := json.Marshal(failure)
			if err != nil || strings.Contains(string(encoded), "link-first") {
				t.Fatalf("serialized refusal exposes holder: %s, %v", encoded, err)
			}
		})
	}
	t.Run("cross product", func(t *testing.T) {
		err := ApplyOperation(context.Background(), s, Operation{
			Events: []Event{
				productCreatedEvent("product-other", "create-product-other"),
				projectCreatedEvent("project-other", "create-project-other"),
				operationEvent("other-product-project", "product_project.added", SubjectProduct, "product-other", map[string]any{"product_id": "product-other", "project_id": "project-other", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
				workCreatedEvent("link-other", "create-link-other"),
				operationEvent("membership-link-other", "work_project.added", SubjectWorkItem, "link-other", map[string]any{"work_id": "link-other", "project_id": "project-other", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-other"): 0, VersionRef(SubjectProject, "project-other"): 0, VersionRef(SubjectWorkItem, "link-other"): 0},
		})
		if err != nil {
			t.Fatal(err)
		}
		recordLink(t, s, "link-other", "CON-8200", "uuid-other-product")
		seedLinkWork(t, s, "link-probe")
		err = s.Transact(context.Background(), func(tx *Transaction) error {
			_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: "link-probe", RemoteIssueUUID: "uuid-other-product", HumanKey: "CON-8200", URL: "https://linear.app/example/issue/CON-8200"})
			return err
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidRelation || !strings.Contains(failure.Detail, "another live work item") || strings.Contains(failure.Detail, "link-other") || failure.HoldingWorkID != "link-other" {
			t.Fatalf("cross-product holder must stay anonymous: %v", err)
		}
	})
}

func TestLinearLinkTerminalHolderDoesNotBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedLinkWork(t, s, "link-terminal")
	recordLink(t, s, "link-terminal", "CON-8102", "uuid-link-terminal")
	version := readWorkVersion(t, s, "link-terminal")
	if err := applyWorkEvent(t, s, workTransitionEvent("link-terminal-cancel", "link-terminal", "needed", "cancelled", version, version+1), workVersion("link-terminal", version)); err != nil {
		t.Fatal(err)
	}
	seedLinkWork(t, s, "link-fresh")
	recordLink(t, s, "link-fresh", "CON-8102", "uuid-link-terminal")
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_issue_links WHERE remote_issue_uuid='uuid-link-terminal'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("terminal identity must stay recorded while a fresh live item recaptures: count=%d err=%v", count, err)
	}
}

func TestLinearLinkQ3InvalidKey(t *testing.T) {
	s := openTemp(t)
	_, err := s.QueryQ3(context.Background(), Q3Request{LinearIssueKey: "con-123"})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidFilter || failure.Op != "PM1.Q3" || !strings.Contains(failure.Detail, "CON-801") {
		t.Fatalf("invalid Linear filter: %v", err)
	}
}

func TestLinearLinkWorkHoldsOneIdentity(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedLinkWork(t, s, "link-single")
	recordLink(t, s, "link-single", "CON-8103", "uuid-link-single")
	// Recording the same identity again is a no-op.
	recordLink(t, s, "link-single", "CON-8103", "uuid-link-single")
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: "link-single", RemoteIssueUUID: "uuid-other", HumanKey: "CON-8104", URL: "https://linear.app/example/issue/CON-8104"})
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("different identity error = %v, want %s", err, KindInvalidOperation)
	}
}

func TestLinearLinkReopenRefusesLiveHolder(t *testing.T) {
	s := openTemp(t)
	seedLinkWork(t, s, "link-old")
	recordLink(t, s, "link-old", "CON-8110", "uuid-reopen")
	v := readWorkVersion(t, s, "link-old")
	if err := applyWorkEvent(t, s, workTransitionEvent("close-old", "link-old", "needed", "cancelled", v, v+1), workVersion("link-old", v)); err != nil {
		t.Fatal(err)
	}
	seedLinkWork(t, s, "link-new")
	recordLink(t, s, "link-new", "CON-8110", "uuid-reopen")
	v = readWorkVersion(t, s, "link-old")
	err := applyWorkEvent(t, s, workReopenedEvent("reopen-old", "link-old", "cancelled", v, v+1), workVersion("link-old", v))
	if !hasFailureKind(err, KindInvalidRelation) {
		t.Fatalf("reopen collision = %v, want invalid_relation", err)
	}
	if got := readWorkVersion(t, s, "link-old"); got != v {
		t.Fatalf("refused reopen changed version: %d -> %d", v, got)
	}
	var state string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id='link-old'`).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("refused reopen state %q: %v", state, err)
	}
}

func TestLinearLinkTerminalAttachmentDoesNotClaimLiveIssue(t *testing.T) {
	s := openTemp(t)
	seedLinkWork(t, s, "link-live")
	recordLink(t, s, "link-live", "CON-8111", "uuid-attach")
	seedLinkWork(t, s, "link-past")
	v := readWorkVersion(t, s, "link-past")
	if err := applyWorkEvent(t, s, workTransitionEvent("close-past", "link-past", "needed", "cancelled", v, v+1), workVersion("link-past", v)); err != nil {
		t.Fatal(err)
	}
	recordLink(t, s, "link-past", "CON-8111", "uuid-attach")
}
