package store

// Derived from the current compositions; no retained version inventory exists.
var builtinWorkflowCurrentVersions = func() map[string]int64 {
	versions := make(map[string]int64)
	for _, composition := range builtinWorkflowCompositions() {
		versions[composition.Ref] = composition.Version
	}
	return versions
}()
