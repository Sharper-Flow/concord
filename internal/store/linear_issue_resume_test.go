package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReadLinearLinkByIssueKey(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "work-keyed")
	link := LinearIssueLink{WorkID: "work-keyed", RemoteIssueUUID: "remote-keyed", HumanKey: "EX-3", URL: "https://linear.app/example/issue/EX-3"}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(ctx, tx, link)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadLinearLinkByIssueKey(ctx, link.HumanKey)
	if err != nil || got != link {
		t.Fatalf("reverse link=%+v err=%v want %+v", got, err, link)
	}
	for _, tc := range []struct {
		key  string
		kind FailureKind
	}{
		{"EX-4", KindUnknownScope},
		{"", KindInvalidPayload},
		{"ex-3", KindInvalidPayload},
		{"EX-03", KindInvalidPayload},
		{"EX-0", KindInvalidPayload},
		{" EX-3", KindInvalidPayload},
		{"EX-3\n", KindInvalidPayload},
		{strings.Repeat("E", 63) + "-3", KindInvalidPayload},
	} {
		t.Run(fmt.Sprintf("%q", tc.key), func(t *testing.T) {
			got, err := s.ReadLinearLinkByIssueKey(ctx, tc.key)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || failure.RetrySafe || failure.EffectPossible || got != (LinearIssueLink{}) {
				t.Fatalf("reverse link=%+v err=%+v want %s without effects", got, err, tc.kind)
			}
		})
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || before != after {
		t.Fatalf("lookup changed database: %d -> %d err=%v", before, after, err)
	}
}

func TestReadLinearLinkByIssueKeyRefusesEveryDuplicate(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	var candidates []string
	for i := 0; i < MaxFailureCandidates+1; i++ {
		workID := fmt.Sprintf("work-key-%02d", i)
		seedWork(t, s, workID)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := RecordLinearIssueLinkTx(ctx, tx, LinearIssueLink{WorkID: workID, RemoteIssueUUID: "remote-" + workID, HumanKey: "EX-3", URL: "https://linear.app/example/issue/EX-3"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, workID)
		if i == 0 {
			continue
		}
		got, err := s.ReadLinearLinkByIssueKey(ctx, "EX-3")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindAmbiguousScope || failure.RetrySafe || got != (LinearIssueLink{}) {
			t.Fatalf("duplicate count=%d link=%+v err=%+v", i+1, got, err)
		}
		want := candidates[:min(len(candidates), MaxFailureCandidates)]
		if !reflect.DeepEqual(failure.CandidateIDs, want) {
			t.Fatalf("candidates=%v want %v", failure.CandidateIDs, want)
		}
	}
}

func TestReadLinearLinkByIssueKeyReportsUnavailable(t *testing.T) {
	s := openTemp(t)
	if _, err := s.db.Exec(`ALTER TABLE linear_issue_links RENAME TO linear_issue_links_unavailable`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadLinearLinkByIssueKey(context.Background(), "EX-3")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable || !failure.RetrySafe || got != (LinearIssueLink{}) {
		t.Fatalf("unavailable reverse lookup link=%+v err=%+v", got, err)
	}
}
