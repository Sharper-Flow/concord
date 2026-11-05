package store

import (
	"context"
	"reflect"
	"testing"
)

// CD-0242 D2 retires the exact transition trace. Paging retains only current rows.
func TestQ7ContinuationPagesHoldCurrentEntries(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	const work = "blocked"
	for _, tc := range []struct {
		name      string
		direction string
		kinds     []string
	}{
		{name: "all oldest first", direction: "oldest_first"},
		{name: "all newest first", direction: "newest_first"},
		{name: "relations oldest first", direction: "oldest_first", kinds: []string{"relation"}},
		{name: "relations newest first", direction: "newest_first", kinds: []string{"relation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, err := s.QueryQ7(context.Background(), Q7Request{Work: work, Direction: tc.direction, RecordKinds: tc.kinds, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			var got []HistoryEntry
			cursor := ""
			for len(got) <= len(full.Entries) {
				out, err := s.QueryQ7(context.Background(), Q7Request{Work: work, Direction: tc.direction, RecordKinds: tc.kinds, Limit: 1, Cursor: cursor})
				if err != nil {
					t.Fatalf("page %d: %v", len(got)+1, err)
				}
				got = append(got, out.Entries...)
				if out.NextCursor == nil {
					break
				}
				cursor = *out.NextCursor
			}
			if !reflect.DeepEqual(got, full.Entries) {
				t.Fatalf("pages = %v, want %v with no cursor after the last page", got, full.Entries)
			}
		})
	}
}
