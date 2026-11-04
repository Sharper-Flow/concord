package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeRejectsRetiredCutoverInput(t *testing.T) {
	for _, input := range []string{
		`{"cancel_cutover_pinned_work":true}`,
		`{"plan":true,"cancel_cutover_pinned_work":false}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, root := cliStoreRoot(t)
			code, out, errOut := runUpgradeStdin(t, input)
			if code == 0 || out != "" || !strings.Contains(errOut, `unknown field "cancel_cutover_pinned_work"`) {
				t.Fatalf("retired input: code=%d out=%q err=%q", code, out, errOut)
			}
			if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
				t.Fatalf("refused input created a backup directory or could not inspect it: %v", err)
			}
		})
	}
}
