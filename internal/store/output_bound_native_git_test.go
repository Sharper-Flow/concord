package store

func init() {
	owner := "internal/store/worktree_verify_oracle_git.go"
	// view validates every published identity with nativeGitOID, which also
	// applies the shared commit grammar. Tags are a subset of the bounded
	// source ref inventory, never a truncated list.
	sha := goPatternBound("internal/store/worktrees.go", "worktreeSHAPattern")
	for _, field := range []string{"commit", "tree"} {
		registerOutputBound("/$defs/native_git_view/properties/"+field+"/maxLength", sha)
	}
	for _, field := range []string{"object", "peeled"} {
		registerOutputBound("/$defs/native_git_tag/properties/"+field+"/maxLength", sha)
	}
	registerOutputBound("/$defs/native_git_tag/properties/ref/maxLength", goConstantBound(owner, "nativeGitRefMaxBytes", boundBytes))
	registerOutputBound("/$defs/native_git_view/properties/tags/maxItems", goConstantBound(owner, "nativeGitMaxTags", boundItems))
}
