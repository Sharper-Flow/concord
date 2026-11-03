package gittest

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCommandRefusesImplicitWorkingDirectory(t *testing.T) {
	constructors := map[string]func(string, ...string) (*exec.Cmd, error){
		"command": Command,
		"context": func(dir string, args ...string) (*exec.Cmd, error) {
			return CommandContext(context.Background(), dir, args...)
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			for _, dir := range []string{"", ".", "..", "relative/repo"} {
				cmd, err := construct(dir, "status")
				if cmd != nil || err == nil || !strings.Contains(err.Error(), "test git requires an absolute fixture directory") {
					t.Errorf("directory %q: command %v, error %v", dir, cmd, err)
				}
			}
			dir := t.TempDir()
			cmd, err := construct(dir, "status", "--porcelain")
			if err != nil || cmd == nil {
				t.Fatalf("absolute fixture command: %v", err)
			}
			if cmd.Dir != dir || !reflect.DeepEqual(cmd.Args[1:], []string{"status", "--porcelain"}) {
				t.Fatalf("dir=%q args=%q", cmd.Dir, cmd.Args)
			}
		})
	}
}
