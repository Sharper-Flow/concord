package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFragmentSelectorMatchesExhaustiveOracle(t *testing.T) {
	t.Parallel()
	const (
		incompleteOverhead = 512 + 200
		completeOverhead   = 512 + 40
	)
	sweep := func(t *testing.T, name string, doc []byte, offset int) {
		t.Run(name, func(t *testing.T) {
			if offset >= len(doc) || !utf8.RuneStart(doc[offset]) {
				t.Fatalf("case %s offset %d is not a code-point start of the document", name, offset)
			}
			cost := func(n int) int {
				raw, err := json.Marshal(string(doc[offset : offset+n]))
				if err != nil {
					return 1 << 30
				}
				overhead := incompleteOverhead
				if offset+n == len(doc) {
					overhead = completeOverhead
				}
				return overhead + len(raw)
			}
			remaining := len(doc) - offset
			largestFit := func(capacity int) (best, bestIncomplete int) {
				for n := 1; n <= remaining; n++ {
					if n < remaining && !utf8.RuneStart(doc[offset+n]) {
						continue
					}
					if cost(n) > capacity {
						continue
					}
					if n > best {
						best = n
					}
					if n < remaining && n > bestIncomplete {
						bestIncomplete = n
					}
				}
				return best, bestIncomplete
			}
			maxCost := 0
			for n := 1; n <= remaining; n++ {
				if cost(n) > maxCost {
					maxCost = cost(n)
				}
			}
			terminalDropExercised := false
			for capacity := 1; capacity <= maxCost+2; capacity++ {
				want, bestIncomplete := largestFit(capacity)
				got, err := fragmentLength(doc, offset, func(n int) (bool, error) {
					if n == 0 {
						t.Fatal("the selector evaluated an empty cut")
					}
					return cost(n) <= capacity, nil
				})
				if err != nil {
					t.Fatalf("capacity %d: selector returned an unexpected error: %v", capacity, err)
				}
				if got != want {
					t.Fatalf("capacity %d: selector chose %d, exhaustive legal-boundary oracle chose %d", capacity, got, want)
				}
				if want == remaining && bestIncomplete < remaining {
					terminalDropExercised = true
				}
			}
			if !terminalDropExercised {
				t.Fatalf("case %s never exercised a capacity where only the terminal overhead drop fits the remainder", name)
			}
		})
	}
	sweep(t, "mixed_runle_offset_0", []byte("a"+strings.Repeat("é", 2)+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+`\\\"`+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+"z"), 0)
	sweep(t, "leading_emoji", []byte(strings.Repeat("😀", 2)+"x"+strings.Repeat("中", 2)+`\\`+strings.Repeat("😀", 2)), 0)
	sweep(t, "single_leading_emoji_counterexample", []byte("😀x"), 0)
	sweep(t, "mixed_runle_offset_3", []byte("a"+strings.Repeat("é", 2)+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+`\\\"`+strings.Repeat("中", 2)+strings.Repeat("😀", 2)+"z"), 3)
	sweep(t, "leading_emoji_offset_4", []byte(strings.Repeat("😀", 2)+"x"+strings.Repeat("中", 2)+`\\`+strings.Repeat("😀", 2)), 4)
}

func TestFragmentSelectorPropagatesRenderErrors(t *testing.T) {
	t.Parallel()
	firstFailure := errors.New("first render failed")
	doc := []byte(strings.Repeat("😀", 3) + "x")
	t.Run("first_render_error_stops_the_search", func(t *testing.T) {
		calls := 0
		length, err := fragmentLength(doc, 0, func(int) (bool, error) { calls++; return false, firstFailure })
		if !errors.Is(err, firstFailure) || length != 0 {
			t.Fatalf("first render error surfaced as length=%d err=%v, want the error itself", length, err)
		}
		if calls != 1 {
			t.Fatalf("the search rendered %d times after the first failure, want one", calls)
		}
	})
	t.Run("mid_search_error_surfaces", func(t *testing.T) {
		calls := 0
		length, err := fragmentLength(doc, 0, func(int) (bool, error) {
			calls++
			if calls == 1 {
				return false, nil
			}
			if calls >= 3 {
				return false, firstFailure
			}
			return true, nil
		})
		if !errors.Is(err, firstFailure) || length != 0 {
			t.Fatalf("mid-search render error surfaced as length=%d err=%v, want the error itself", length, err)
		}
		if calls != 3 {
			t.Fatalf("the search rendered %d times, want exactly the failing third call", calls)
		}
	})
}
