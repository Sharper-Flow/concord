package store

import "testing"

type shippedDefinitionDigest struct {
	ref     string
	version int64
	digest  string
}

var shippedDefinitionDigestFixture = []shippedDefinitionDigest{
	{ref: "workflow.implementation", version: 1, digest: "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155"},
	{ref: "workflow.implementation", version: 2, digest: "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89"},
	{ref: "workflow.break_fix", version: 1, digest: "sha256:aefce865f350345dc41fc1e2e988e7d5e246fa7fd560335399cf8c826e4cc35a"},
	{ref: "workflow.break_fix", version: 2, digest: "sha256:d7f8d8cc8b951e74751ddafe95c7b9c9d65e606cd73c41b2ceadd5fa2cdf29cb"},
	{ref: "workflow.research", version: 1, digest: "sha256:adeb334ee4eb08e1907b2f36c618d809675a81f325266733142e697a90c108b9"},
	{ref: "workflow.research", version: 2, digest: "sha256:7a987b5e2cbc9bafd7a80e92345efa35b331ccaa533aefe4722024485be57e4a"},
	{ref: "workflow.architecture_spike", version: 1, digest: "sha256:0de0f3007629a509f8d6e289ce424f33aaaa9c160693a530898f6c039149a3fa"},
	{ref: "workflow.architecture_spike", version: 2, digest: "sha256:7f7a7c0802daf0bed8e65744ef12480f14045f1d8c4048b80d35021448b83c07"},
	{ref: "workflow.ops_runbook", version: 1, digest: "sha256:d1218c37554f1412b55445b306d5141d11789c7ff78fe0a656f6d15959357ced"},
	{ref: "workflow.ops_runbook", version: 2, digest: "sha256:4b19ba4c81ffcb1fef8f2da8b45c47c9a0121f9531dcffabdd15af25c5b82dab"},
	{ref: "workflow.static_analysis", version: 1, digest: "sha256:d0bc28751b65cb1ae5a0dc31e8db177a6ffe4480f39725fb16e467d88ef4c038"},
	{ref: "workflow.static_analysis", version: 2, digest: "sha256:161b3b2b85d075b069cb6b9a9dda26cf226c82ba4e7ee5e7946a2792cd76ecac"},
	{ref: "workflow.generic_one_off", version: 1, digest: "sha256:c2b8b4c8ef11b2de08912f7c82faa91dffe6a2fbe4ddcef924ff4b393da578b3"},
	{ref: "workflow.generic_one_off", version: 2, digest: "sha256:273c82c0a0cf6c17d231f1be898ff74c6158f8036985cb3e1666b8f12c1b7895"},
}

func TestBuiltinDefinitionDigestsMatchShippedFixture(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, fixture := range shippedDefinitionDigestFixture {
		registered, ok := registry.Lookup(fixture.ref, fixture.version)
		if !ok {
			t.Fatalf("%s v%d is not registered", fixture.ref, fixture.version)
		}
		if registered.Digest != fixture.digest {
			t.Fatalf("%s v%d digest=%s, want %s", fixture.ref, fixture.version, registered.Digest, fixture.digest)
		}
	}
}
