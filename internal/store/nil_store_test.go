package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestUnopenedStoreReturnsTypedFailure pins the guard placement for methods
// that delegate to a queryer-taking core.
//
// The guard belongs on the method, not the core. A nil *sql.DB placed in a
// queryer parameter becomes a non-nil interface holding a nil pointer, so a
// core-side "q == nil" test cannot fire and the call panics inside the driver
// instead of returning a typed failure. A nil receiver panics even earlier,
// when the method evaluates s.db to build the argument.
func TestUnopenedStoreReturnsTypedFailure(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(*Store) error
	}{
		{"ReadWorkItemSummary", func(s *Store) error {
			_, err := s.ReadWorkItemSummary(ctx, "work-1")
			return err
		}},
		{"DomainEventWatermark", func(s *Store) error {
			_, err := s.DomainEventWatermark(ctx)
			return err
		}},
		{"KnowledgeIndexWatermark", func(s *Store) error {
			_, err := s.KnowledgeIndexWatermark(ctx, "prod-1", "loc-1", "refs/heads/main")
			return err
		}},
		{"AwaitHealthForWork", func(s *Store) error {
			_, err := s.AwaitHealthForWork(ctx, "work-1", time.Time{})
			return err
		}},
		{"OverdueAwaitsInProduct", func(s *Store) error {
			_, err := s.OverdueAwaitsInProduct(ctx, "prod-1", time.Time{}, 10)
			return err
		}},
		{"EntityVersion", func(s *Store) error {
			_, err := s.EntityVersion(ctx, SubjectProduct, "prod-1")
			return err
		}},
		{"ResourceClaims", func(s *Store) error {
			_, err := s.ResourceClaims(ctx, "key-1", "prod-1", 10)
			return err
		}},
		{"ObservationsForWork", func(s *Store) error {
			_, err := s.ObservationsForWork(ctx, "work-1", 10)
			return err
		}},
		{"SyncDurable", func(s *Store) error {
			return s.SyncDurable(ctx)
		}},
		{"MessagesForWork", func(s *Store) error {
			_, err := s.MessagesForWork(ctx, "work-1", 10)
			return err
		}},
		{"UnreadMessageCount", func(s *Store) error {
			_, err := s.UnreadMessageCount(ctx, "work-1")
			return err
		}},
		{"ActiveWorkInProduct", func(s *Store) error {
			_, err := s.ActiveWorkInProduct(ctx, "prod-1", 10)
			return err
		}},
		{"ExternalObservationsForWork", func(s *Store) error {
			_, err := s.ExternalObservationsForWork(ctx, "work-1", time.Time{}, 10)
			return err
		}},
		{"WorkerAttemptByID", func(s *Store) error {
			_, err := s.WorkerAttemptByID(ctx, "attempt-1")
			return err
		}},
		{"QueryLawConflictsAtHome", func(s *Store) error {
			_, err := s.QueryLawConflictsAtHome(ctx, "proj-1", "loc-1", []string{"law-1"})
			return err
		}},
		{"CheckMandatedLawsAtHome", func(s *Store) error {
			return s.CheckMandatedLawsAtHome(ctx, "proj-1", "loc-1", []string{"law-1"}, nil, false)
		}},
		{"BlockedSessions", func(s *Store) error {
			_, err := s.BlockedSessions(ctx, time.Time{}, nil, 10)
			return err
		}},
		{"ResolveKnowledgeQueryHome", func(s *Store) error {
			_, err := s.ResolveKnowledgeQueryHome(ctx, "", "", KnowledgeHome{}, "knowledge_home")
			return err
		}},
		{"QueryDomainList", func(s *Store) error {
			_, err := s.QueryDomainList(ctx, DomainListRequest{Product: "prod-1"})
			return err
		}},
		{"QueryDomainDetail", func(s *Store) error {
			_, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "prod-1", Domain: "domain-1"})
			return err
		}},
		{"QueryDomainActiveWork", func(s *Store) error {
			_, err := s.QueryDomainActiveWork(ctx, DomainActiveWorkRequest{Product: "prod-1", Domain: "domain-1"})
			return err
		}},
		{"QueryDomainAttachments", func(s *Store) error {
			_, err := s.QueryDomainAttachments(ctx, DomainAttachmentsRequest{Product: "prod-1", Domain: "domain-1"})
			return err
		}},
		{"QueryDomainOverlaps", func(s *Store) error {
			_, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: "prod-1"})
			return err
		}},
	} {
		for _, receiver := range []struct {
			label string
			store *Store
		}{
			{"nil receiver", nil},
			{"nil database handle", &Store{}},
		} {
			t.Run(tc.name+"/"+receiver.label, func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked on an unopened store: %v", tc.name, r)
					}
				}()
				err := tc.call(receiver.store)
				if err == nil {
					t.Fatalf("%s returned no error on an unopened store", tc.name)
				}
				var failure *Failure
				if !errors.As(err, &failure) {
					t.Fatalf("%s returned an untyped error on an unopened store: %v", tc.name, err)
				}
			})
		}
	}
}
