package store

import (
	"context"
	"testing"
)

// The unopened store refuses every retained read with one typed failure. The
// retired Q9/Q10 knowledge-search rows left with their surface.
func TestUnopenedStoreQueryMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"QueryQ1", func(s *Store) error {
			_, err := s.QueryQ1(ctx, Q1Request{})
			return err
		}},
		{"QueryQ2", func(s *Store) error {
			_, err := s.QueryQ2(ctx, Q2Request{})
			return err
		}},
		{"QueryQ3", func(s *Store) error {
			_, err := s.QueryQ3(ctx, Q3Request{})
			return err
		}},
		{"QueryQ4", func(s *Store) error {
			_, err := s.QueryQ4(ctx, Q4Request{})
			return err
		}},
		{"QueryQ5", func(s *Store) error {
			_, err := s.QueryQ5(ctx, Q5Request{})
			return err
		}},
		{"QueryQ6", func(s *Store) error {
			_, err := s.QueryQ6(ctx, Q6Request{})
			return err
		}},
		{"QueryQ7", func(s *Store) error {
			_, err := s.QueryQ7(ctx, Q7Request{})
			return err
		}},
		{"QueryQ8", func(s *Store) error {
			_, err := s.QueryQ8(ctx, Q8Request{})
			return err
		}},
	})
}
