package store

import "testing"

func TestSessionRelocationCountsSurviveUpgrade(t *testing.T) {
	t.Parallel()
	assertSessionWindowCorpus144(t)
}
