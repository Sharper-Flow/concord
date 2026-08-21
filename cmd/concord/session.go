package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	selectedProductEnv = "CONCORD_SELECTED_PRODUCT_ID"
	selectedWorkEnv    = "CONCORD_SELECTED_WORK_ID"
)

var sessionIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$`)

type sessionBootstrapFunc func(context.Context, string, string, string) ([]byte, error)
type sessionRunnerFunc func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error
type sessionAgentIdentityFunc func() error

// hostLaneAgentIdentity asserts the registered lanes against the host the
// session will start on.
func hostLaneAgentIdentity() error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return verifyLaneAgentIdentity(os.Getenv("HOME"), cwd, store.BuiltinLaneDefinitions())
}

func deriveSessionBoot(ctx context.Context, database, productID, workID string) ([]byte, error) {
	s, err := store.Open(ctx, database)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	snapshot, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: workID, Limit: 1})
	if err != nil {
		return nil, err
	}
	return sessionboot.Build(productID, snapshot)
}

func runOpenCode(ctx context.Context, argv, env []string, in io.Reader, out, errOut io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}

func runSessionCommand(args []string, in io.Reader, out, errOut io.Writer, terminal bool, bootstrap sessionBootstrapFunc, runner sessionRunnerFunc, identity sessionAgentIdentityFunc) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, "concord session: unsupported arguments")
		return 2
	}
	if !terminal {
		writeDiagnostic(errOut, "concord session requires an interactive TTY")
		return 2
	}
	productID, workID := os.Getenv(selectedProductEnv), os.Getenv(selectedWorkEnv)
	if !sessionIdentity.MatchString(productID) || (workID != "" && !sessionIdentity.MatchString(workID)) {
		writeDiagnostic(errOut, "concord session: launcher identity is missing or invalid")
		return 2
	}
	if err := identity(); err != nil {
		writeDiagnostic(errOut, "concord session: "+err.Error())
		return 2
	}
	prompt := "Concord identity: product_id=" + productID
	if workID != "" {
		path, err := databasePath()
		if err != nil {
			writeDiagnostic(errOut, err.Error())
			return 1
		}
		packet, err := bootstrap(context.Background(), path, productID, workID)
		if err != nil {
			writeDiagnostic(errOut, "concord session: "+err.Error())
			return 1
		}
		prompt = "Concord session boot packet (core-derived authority at its watermark; reread concord_work_trace.continuity before consequential action):\n" + string(packet)
	}
	if err := runner(context.Background(), []string{"opencode", "--prompt", prompt}, os.Environ(), in, out, errOut); err != nil {
		writeDiagnostic(errOut, fmt.Sprintf("concord session: opencode: %v", err))
		return 1
	}
	return 0
}
