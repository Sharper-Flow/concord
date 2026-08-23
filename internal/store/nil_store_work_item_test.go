package store

import (
	"context"
	"testing"
	"time"
)

func TestUnopenedStoreWorkItemMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"ReadWorkItemSummary", func(s *Store) error {
			_, err := s.ReadWorkItemSummary(ctx, "work-1")
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
		{"ObservationsForWork", func(s *Store) error {
			_, err := s.ObservationsForWork(ctx, "work-1", 10)
			return err
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
		{"LatestWorkflowContractVersion", func(s *Store) error {
			_, err := s.LatestWorkflowContractVersion(ctx, "work-1")
			return err
		}},
		{"ActiveWorkflowContract", func(s *Store) error {
			_, err := s.ActiveWorkflowContract(ctx, "work-1")
			return err
		}},
		{"WorkVersion", func(s *Store) error {
			_, err := s.WorkVersion(ctx, "work-1")
			return err
		}},
		{"TerminalWorkVersion", func(s *Store) error {
			_, err := s.TerminalWorkVersion(ctx, "work-1")
			return err
		}},
		{"PendingOperationForWork", func(s *Store) error {
			_, err := s.PendingOperationForWork(ctx, "work-1")
			return err
		}},
	})
}
