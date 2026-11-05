package store

import (
	"context"
	"errors"
	"testing"
)

// One live work item per Linear issue (CON-1571 end state 1): the Linear
// identity rides linear_issue_links, capture writes the link row in its own
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
				t.Fatalf("collision error = %v, want typed %s naming the live holder", err, KindInvalidRelation)
			}
			if !contains(failure.Detail, "link-first") {
				t.Fatalf("refusal must name the live holder: %+v", failure)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
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
