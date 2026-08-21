package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSessionBootPassesCorePacketToOpenCodeBeforeSessionStarts(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	var argv []string
	bootstrapCalls := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		bootstrapCalls++
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:4",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 2,
		})
	}
	runner := func(_ context.Context, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		argv = append([]string(nil), got...)
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	if len(argv) != 3 || argv[0] != "opencode" || argv[1] != "--prompt" {
		t.Fatalf("argv=%q", argv)
	}
	start := strings.IndexByte(argv[2], '{')
	if start < 0 || !strings.Contains(argv[2][:start], "core-derived authority") {
		t.Fatalf("prompt omitted boot header: %q", argv[2])
	}
	if err := sessionboot.Validate([]byte(argv[2][start:])); err != nil {
		t.Fatalf("prompt packet invalid: %v", err)
	}
}

func TestSessionBootFailsClosedBeforeOpenCodeOnPacketFailure(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	runs := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		return nil, errors.New("manifest digest mismatch")
	}
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }); code == 0 {
		t.Fatal("packet failure started session")
	}
	if runs != 0 || !strings.Contains(errOut.String(), "manifest digest mismatch") {
		t.Fatalf("runs=%d stderr=%q", runs, errOut.String())
	}
}

func TestProductOnlySessionRemainsIdentityOnly(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "")
	bootstrapCalls := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	var prompt string
	runner := func(_ context.Context, argv []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		prompt = argv[2]
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if bootstrapCalls != 0 || prompt != "Concord identity: product_id=product-1" {
		t.Fatalf("calls=%d prompt=%q", bootstrapCalls, prompt)
	}
}

func TestSessionRoutesBeforeJSONAndRejectsNonTTY(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"session"}, strings.NewReader("not json"), &out, &errOut); code != 2 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "interactive TTY") || strings.Contains(errOut.String(), "JSON") {
		t.Fatalf("session route diagnostic=%q", errOut.String())
	}
}

func TestSessionRefusesToStartWhenRequiredAgentIdentityIsAbsent(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	identity := func() error { return verifyLaneAgentIdentity("", "", store.BuiltinLaneDefinitions()) }
	var out, errOut bytes.Buffer

	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, identity)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if runs != 0 {
		t.Fatalf("opencode started %d time(s) despite absent agent identity", runs)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap ran %d time(s); identity must be asserted before packet derivation", bootstrapCalls)
	}
	if !strings.Contains(errOut.String(), "required agent identity is absent") {
		t.Fatalf("diagnostic did not name the absence: %q", errOut.String())
	}
	for _, lane := range store.BuiltinLaneDefinitions() {
		if !strings.Contains(errOut.String(), laneAgentFileName(lane.ID)) {
			t.Fatalf("diagnostic omitted lane %q: %q", lane.ID, errOut.String())
		}
	}
}
