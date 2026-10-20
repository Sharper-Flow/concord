package store

// NativeSelectorRemedyFixture exposes the real selector admission to the
// external-package serialization harness without adding a production API.
func NativeSelectorRemedyFixture(runner string, argv []string) ([]string, error) {
	m := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: runner,
		Cases: map[string][]string{"case:first": {"zeta", "alpha"}, "case:second": {"alpha"}},
	}
	return nativeTargetSelector(m, OracleControl{CaseIDs: []string{"case:second", "case:first"}, Argv: argv})
}
