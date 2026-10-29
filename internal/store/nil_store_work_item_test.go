package store

import (
	"context"
	"testing"
	"time"
)

func TestUnopenedStoreWorkItemMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"ReadWorkItemSummary", func(s *Store) error {
			_, err := s.ReadWorkItemSummary(ctx, "work-1")
			return err
		}},
		{"ReadWorkObservations", func(s *Store) error {
			_, err := s.ReadWorkObservations(ctx, WorkObservationsRequest{WorkID: "work-1", Limit: 10})
			return err
		}},
		{"MessagesForWork", func(s *Store) error {
			_, err := s.MessagesForWork(ctx, "work-1", 10)
			return err
		}},
		{"ExternalObservationsForWork", func(s *Store) error {
			_, err := s.ExternalObservationsForWork(ctx, "work-1", time.Time{}, 10)
			return err
		}},
		{"readSnapshot latestWorkflowContractVersion", func(s *Store) error {
			return s.readSnapshot(ctx, func(q queryer) error {
				_, innerErr := latestWorkflowContractVersion(ctx, q, "work-1")
				return innerErr
			})
		}},
		{"ActiveWorkflowContract", func(s *Store) error {
			_, err := s.ActiveWorkflowContract(ctx, "work-1")
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
