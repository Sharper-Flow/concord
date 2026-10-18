package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// declaredTestSupportPackages reads the package exceptions of the reachability
// manifest. scripts/check-reachability.py proves every declared package sits
// outside the shipped command's dependency set, so the production guards in
// this package skip those files and refuse a production import of them. The
// manifest is the one list; the guards keep no copy of it.
var declaredTestSupportPackages = sync.OnceValue(func() map[string]bool {
	raw, err := os.ReadFile(filepath.Join(txScopeRepoRoot(), ".concord", "docs", "reachability-exceptions.v1.json"))
	if err != nil {
		panic("read the reachability manifest: " + err.Error())
	}
	var manifest struct {
		Exceptions []struct {
			Packages []string `json:"packages"`
		} `json:"exceptions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		panic("decode the reachability manifest: " + err.Error())
	}
	packages := map[string]bool{}
	for _, exception := range manifest.Exceptions {
		for _, pkg := range exception.Packages {
			packages[pkg] = true
		}
	}
	if len(packages) == 0 {
		panic("the reachability manifest declares no test-support packages")
	}
	return packages
})

// testSupportPackage reports whether a repository-relative package directory
// is a declared test-support package.
func testSupportPackage(dir string) bool {
	return declaredTestSupportPackages()[filepath.ToSlash(dir)]
}
