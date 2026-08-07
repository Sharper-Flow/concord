package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sharper-flow/concord/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run handles the deliberately small bootstrap CLI surface.
func run(args []string, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(out, version.Value)
		return 0
	}
	if len(args) == 0 {
		return 0
	}

	_, _ = fmt.Fprintf(errOut, "concord: unsupported arguments: %s\n", strings.Join(args, " "))
	return 2
}
