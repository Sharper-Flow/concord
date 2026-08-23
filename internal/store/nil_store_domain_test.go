package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreDomainMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"DomainEventWatermark", func(s *Store) error {
			_, err := s.DomainEventWatermark(ctx)
			return err
		}},
		{"EntityVersion", func(s *Store) error {
			_, err := s.EntityVersion(ctx, SubjectProduct, "prod-1")
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
	})
}
