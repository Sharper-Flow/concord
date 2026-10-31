package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRemovedPredecessorCommands(t *testing.T) {
	for _, args := range [][]string{{"predecessor-import"}, {"predecessor-inventory"}, {"predecessor", "import"}, {"predecessor", "inventory"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runWithInput(args, strings.NewReader(`{}`), &out, &errOut); code != 2 {
				t.Errorf("removed command exit = %d, want 2; stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}
