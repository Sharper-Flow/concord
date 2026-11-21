package main

import (
	"strings"
	"testing"
)

// stubResolution returns the host-command resolution a successful bootstrap
// probe would carry: the bare host command and the supplied registry
// document. Tests own the registry shape this way; none of them may shell
// out. The probe argv is recorded nowhere here because the resolution
// already ran by the time these tests observe it.
func stubResolution(document string) hostCommandResolution {
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...), Registry: []byte(document)}
}

func TestHostRegistrationAcceptsV2Registry(t *testing.T) {
	document := `[{"id":"concord-orchestrator","mode":"primary","hidden":true,"permissions":[]}]`
	if err := verifyHostRegistersHandle(stubResolution(document), "concord-orchestrator"); err != nil {
		t.Fatalf("resolved primary identity refused: %v", err)
	}
}

// A registry that lists the handle as an enabled primary agent is the only
// state that lets a session proceed. The host answers an unregistered name
// with the operator's default agent and exits zero, so the
// selection Concord is about to make has to be proved registered before the
// session starts, not after.
func TestHostRegistrationAcceptsAnEnabledPrimaryHandle(t *testing.T) {
	for _, mode := range []string{"primary", "all"} {
		t.Run(mode, func(t *testing.T) {
			document := `[{"id":"concord-orchestrator","mode":"` + mode + `"}]`
			if err := verifyHostRegistersHandle(stubResolution(document), "concord-orchestrator"); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

// Each row here is a way a definition resolves on disk while the handle the
// session would select is not startable. Every one must refuse, and the
// diagnostic must name the handle and what was observed, because there is no
// degraded start and the operator cannot see the substitution.
func TestHostRegistrationRefusesUnstartableHandles(t *testing.T) {
	cases := []struct {
		name     string
		document string
		observed string
	}{
		{
			name:     "handle missing or disabled",
			document: `[{"id":"build","mode":"primary"}]`,
			observed: "not registered (missing or disabled)",
		},
		{
			name:     "handle registered as a subagent",
			document: `[{"id":"concord-orchestrator","mode":"subagent"}]`,
			observed: "subagent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyHostRegistersHandle(stubResolution(tc.document), "concord-orchestrator")
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
	document := `[{"id":"op-renamed","mode":"primary"}]`
	err := verifyHostRegistersHandle(stubResolution(document), "op-renamed # trailing comment")
	if err == nil {
		t.Fatal("verification accepted a handle the registry does not carry")
	}
	if !strings.Contains(err.Error(), "op-renamed # trailing comment") {
		t.Fatalf("diagnostic omits the derived handle: %q", err.Error())
	}
}

// An unreadable registry is not an absent constraint. The resolution
// carrying no document means Concord cannot establish the property, and
// there is no degraded start, so the session refuses and says why.
// Each row models the resolution a failed probe leaves behind: no document to
// read.
func TestHostRegistrationRefusesWhenTheRegistryCannotBeRead(t *testing.T) {
	cases := []struct {
		name     string
		document string
	}{
		{name: "resolution carried no document"},
		{name: "resolution carried malformed json", document: "{not json"},
		{name: "resolution carried an object", document: `{"default_agent":"build"}`},
		{name: "null registry", document: `null`},
		{name: "missing id", document: `[{"mode":"primary"}]`},
		{name: "missing mode", document: `[{"id":"concord-orchestrator"}]`},
		{name: "wrong id type", document: `[{"id":1,"mode":"primary"}]`},
		{name: "wrong mode type", document: `[{"id":"concord-orchestrator","mode":true}]`},
		{name: "unknown mode", document: `[{"id":"concord-orchestrator","mode":"other"}]`},
		{name: "duplicate id", document: `[{"id":"concord-orchestrator","mode":"primary"},{"id":"concord-orchestrator","mode":"subagent"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyHostRegistersHandle(stubResolution(tc.document), "concord-orchestrator")
			if err == nil {
				t.Fatal("verification proceeded without reading the registry")
			}
			if !strings.Contains(err.Error(), "concord-orchestrator") {
				t.Fatalf("diagnostic omits the required handle: %q", err.Error())
			}
		})
	}
}

// The registration check reads the document the configured command resolved,
// not the bare host's. A wrapper whose own registry does not register the
// handle refuses even when the bare host's registry would have admitted it —
// the verified registry is the executing registry.
func TestHostRegistrationReadsTheDocumentTheConfiguredCommandResolved(t *testing.T) {
	bareRegistersOnly := stubResolution(`[{"id":"concord-orchestrator","mode":"primary"}]`)
	configuredDoesNot := hostCommandResolution{
		Command:  []string{"host-wrapper", "--profile", "work"},
		Registry: []byte(`[{"id":"someone-else","mode":"primary"}]`),
	}
	if err := verifyHostRegistersHandle(bareRegistersOnly, "concord-orchestrator"); err != nil {
		t.Fatalf("bare registry refused a registered handle: %v", err)
	}
	err := verifyHostRegistersHandle(configuredDoesNot, "concord-orchestrator")
	if err == nil {
		t.Fatal("verification used a registry the configured command does not resolve")
	}
	if !strings.Contains(err.Error(), "not registered") || !strings.Contains(err.Error(), "host-wrapper --profile work debug agents") {
		t.Fatalf("diagnostic=%q, want the configured probe argv and the refusal", err.Error())
	}
}
