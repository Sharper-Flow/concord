package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubRegistryProbe returns a hostRegistryProbeFunc that yields the supplied
// document and error without starting a host process. Tests own the registry
// shape this way; none of them may shell out.
func stubRegistryProbe(document string, err error) hostRegistryProbeFunc {
	return func(context.Context, string) ([]byte, error) {
		return []byte(document), err
	}
}

// A registry that lists the handle as an enabled primary agent is the only
// state that lets a session proceed. The host answers an unregistered name
// with the operator's default agent and exits zero (CD-0049 D2), so the
// selection Concord is about to make has to be proved registered before the
// session starts, not after.
func TestHostRegistrationAcceptsAnEnabledPrimaryHandle(t *testing.T) {
	for _, mode := range []string{"primary", "all"} {
		t.Run(mode, func(t *testing.T) {
			document := `{"agent":{"concord-orchestrator":{"mode":"` + mode + `"}}}`
			probe := stubRegistryProbe(document, nil)
			if err := verifyHostRegistersHandle(context.Background(), probe, "", "concord-orchestrator"); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

// Each row here is a way a definition resolves on disk while the handle the
// session would select is not startable. Every one must refuse, and the
// diagnostic must name the handle and what was observed, because CD-0049 D4
// admits no degraded start and the operator cannot see the substitution.
func TestHostRegistrationRefusesUnstartableHandles(t *testing.T) {
	cases := []struct {
		name     string
		document string
		observed string
	}{
		{
			name:     "handle absent from the registry",
			document: `{"agent":{"build":{"mode":"primary"}}}`,
			observed: "not registered",
		},
		{
			name:     "handle disabled by configuration",
			document: `{"agent":{"concord-orchestrator":{"mode":"primary","disable":true}}}`,
			observed: "disabled",
		},
		{
			name:     "handle registered as a subagent",
			document: `{"agent":{"concord-orchestrator":{"mode":"subagent"}}}`,
			observed: "subagent",
		},
		{
			name:     "registry declares no agents at all",
			document: `{"agent":{}}`,
			observed: "not registered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := stubRegistryProbe(tc.document, nil)
			err := verifyHostRegistersHandle(context.Background(), probe, "", "concord-orchestrator")
			if err == nil {
				t.Fatal("verification accepted a handle the host cannot start")
			}
			for _, fragment := range []string{"concord-orchestrator", tc.observed} {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("diagnostic %q omits %q", err.Error(), fragment)
				}
			}
		})
	}
}

// A handle derived from a spelling the definition scan reads differently from
// the host lands here: the scan yields a name the registry does not carry, so
// the lookup misses and the session refuses. That is the whole point of
// checking the registry rather than trusting the scan — a divergence becomes
// a refusal instead of a silent substitution.
func TestHostRegistrationRefusesAHandleTheScanReadDifferently(t *testing.T) {
	document := `{"agent":{"op-renamed":{"mode":"primary"}}}`
	probe := stubRegistryProbe(document, nil)
	err := verifyHostRegistersHandle(context.Background(), probe, "", "op-renamed # trailing comment")
	if err == nil {
		t.Fatal("verification accepted a handle the registry does not carry")
	}
	if !strings.Contains(err.Error(), "op-renamed # trailing comment") {
		t.Fatalf("diagnostic omits the derived handle: %q", err.Error())
	}
}

// An unreadable registry is not an absent constraint. The probe failing means
// Concord cannot establish the property, and CD-0049 D4 gives no degraded
// start, so the session refuses and says why.
func TestHostRegistrationRefusesWhenTheRegistryCannotBeRead(t *testing.T) {
	cases := []struct {
		name     string
		document string
		err      error
	}{
		{name: "probe failed", document: "", err: errors.New("exec: opencode: not found")},
		{name: "probe returned no document", document: "", err: nil},
		{name: "probe returned malformed json", document: "{not json", err: nil},
		{name: "probe returned no agent map", document: `{"default_agent":"build"}`, err: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := stubRegistryProbe(tc.document, tc.err)
			err := verifyHostRegistersHandle(context.Background(), probe, "", "concord-orchestrator")
			if err == nil {
				t.Fatal("verification proceeded without reading the registry")
			}
			if !strings.Contains(err.Error(), "concord-orchestrator") {
				t.Fatalf("diagnostic omits the required handle: %q", err.Error())
			}
		})
	}
}
