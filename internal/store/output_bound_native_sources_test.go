package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Native-family output-bound source rows for the structural output-bound
// ledger: the active_worktree_verify_lease, worktree_inspect_result, and
// worktree_verify_result results, and every native_oracle_* definition they
// publish (native_oracle_result through the worktree_verify mutation result,
// native_oracle_output_page through the worktree_inspect read result,
// native_oracle_preparation through the continuity, work-pin, and
// worker-packet read views, and active_worktree_verify_lease through the
// continuity read).
//
// Row contract: every finite source reads the actual input schema, Go guard
// constant, or derivation owner in current source; composite writers are
// covered by the one guard they all pass through. Family-local derived
// evaluators (kind "derived") inspect the exact live AST of the named
// writer and fail closed when the derivation changes shape. Rows whose
// field has no per-field producer guard at all are registered as unbounded,
// byte-ceiling only with the read's typed refusal owner; they fail while a
// cap stands and pass once it is removed.
//
// Known, reported producer mismatches this file registers honestly rather
// than hides:
//   - the ambient Project identity has no admission guard anywhere, so the
//     project_id rows are unbounded, byte-ceiling only and fail while their
//     schema caps stand.
//   - the continuity lease path projects retained worktree_verify_leases
//     rows with no existence check and no length guard on the column, so
//     that row is unbounded, byte-ceiling only and fails while its schema
//     cap stands.
//
// Whole-record aggregates (the 48 KiB result_json fallback chain, the 64 KiB
// plan cap, the 51200-byte agent envelope) are the byte ceiling the ruling
// keeps; they are not per-field guards and no row cites one as a finite
// source.

func init() {
	// The named Go constants every verify-command guard compares against
	// (internal/store/worktrees.go:validateWorktreeVerifyCommand; the target
	// plan stages and check argv reach the same guard through
	// worktree_verify_oracle_host_plan.go:stages and
	// worktree_verify_oracle_host.go:validateNativeTargetCheckArgv).
	commandArgs := goConstantBound("internal/store/worktrees.go", "maxWorktreeVerifyCommandArgs", boundItems)
	commandPart := goConstantBound("internal/store/worktrees.go", "maxWorktreeVerifyCommandPart", boundBytes)
	// The operative output preview cap: no caller sets MaxOutputBytes, so
	// VerifyWorktree always falls back to this constant, and the native
	// capture writer clamps the preview with the same constant.
	verifyOutputBytes := goConstantBound("internal/store/worktrees.go", "defaultWorktreeVerifyBytes", boundBytes)
	// Repository-path admission: validateWorkContextRepoPath compares
	// len(path) against this constant; it runs on every pinned oracle file
	// path (worktree_verify_oracle_go.go:nativeGitFile, and
	// worktree_verify_oracle_host.go:nativeTargetContainedName for the target
	// family) and on every recorded control cwd
	// (worker_oracle.go:validateOracleControlDirectory).
	repoPathBytes := goConstantBound("internal/store/work_context.go", "workContextPathMaxBytes", boundBytes)
	// Target witness admission: worktree_verify_oracle_host.go:
	// nativeTargetCaseWitnesses compares len(witness) against this constant,
	// and the execute join requires observed witnesses to equal the selected
	// set (worktree_verify_oracle_host_run.go), so every published witness id
	// carries the same bound.
	witnessBytes := goConstantBound("internal/store/worktree_verify_oracle_host.go", "nativeTargetWitnessMaxBytes", boundBytes)
	// Target stage budget: worktree_verify_oracle_host_plan.go:stages refuses
	// any plan over this many stages for either phase; the Go family emits a
	// fixed four-prepare/six-execute literal set strictly below it
	// (worktree_verify_oracle_go.go:normalizeNativeGoOraclePlan).
	stageBudget := goConstantBound("internal/store/worktree_verify_oracle_host.go", "nativeTargetStageBudget", boundItems)
	// Anchored grammar admissions. deriveWorktreeLocation validates every
	// derived branch through worktreeBranchPattern
	// (internal/store/worktrees.go:ValidateWorktreeClaimIntent), and
	// nativePreparationJoinsValid re-checks subject_commit and manifest_blob
	// through worktreeSHAPattern
	// (internal/store/worktree_verify_oracle.go:nativePreparationJoinsValid),
	// so every published value of each grammar carries its largest accepted
	// width.
	branchBound := goPatternBound("internal/store/worktrees.go", "worktreeBranchPattern")
	shaBound := goPatternBound("internal/store/worktrees.go", "worktreeSHAPattern")
	// Literal Go witness paths pass nativeGoWitnessValid's byte guard;
	// nativeOracleCaseTests refuses len(names) > 8 per case;
	// nativeTargetCaseWitnesses refuses len(ids) > 8 per case; and
	// nativeGoHarnessReceiptMatches refuses len(o.Files) > 32 for either
	// family's pinned file inventory.
	testNameBound := goInlineBound("internal/store/native_recipe_case.go", "nativeGoWitnessValid", "len(name)", boundBytes)
	filesBound := nativeRejectUpperBound("internal/store/native_go_package.go", "nativeGoHarnessReceiptMatches", "len(o.Files)", boundItems)
	caseMapGoBound := goInlineBound("internal/store/worktree_verify_oracle_go.go", "nativeOracleCaseTests", "len(names)", boundItems)
	caseMapTargetBound := goInlineBound("internal/store/worktree_verify_oracle_host.go", "nativeTargetCaseWitnesses", "len(ids)", boundItems)
	// The output page: readNativeVerifyOutput base64-encodes at most
	// nativeOraclePageLimit retained bytes per page.
	dataBase64Bound := base64Bound(goConstantBound("internal/store/worktree_verify_output.go", "nativeOraclePageLimit", boundBytes))

	// -- worktree_inspect_result (concord_work_browse.worktree_inspect read) --

	registerOutputBound("/$defs/worktree_inspect_result/properties/branch/maxLength", branchBound)
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/0/properties/branch/maxLength", branchBound)
	// The oracle_output arm never assigns these fields: readNativeVerifyOutput
	// constructs only WorkID, ProjectID, and Mode, so published values are the
	// empty string. The evaluator fails closed if any of the three members is
	// ever assigned or keyed.
	inspectVacuous := nativeNeverAssignedDerived()
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/1/properties/branch/maxLength", inspectVacuous)

	// The projecting reads validate existence first and the probe is the
	// successful kernel proof: both routes gate the result behind an
	// error-propagating probeWorktreeReachable whose first command is
	// git -C entry.Path through ExecGitRunner, so a projected path was
	// accepted whole by the kernel and carries at most syscall.PathMax-1
	// bytes (pinned toolchain constant, Linux amd64 release platform only).
	// The continuity lease path has no such gate and stays unbounded above.
	pathBound := nativePathMaxProbeDerived()
	registerOutputBound("/$defs/worktree_inspect_result/properties/path/maxLength", pathBound)
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/0/properties/path/maxLength", pathBound)
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/1/properties/path/maxLength", inspectVacuous)

	// Every inspect mode slices content with boundText at the 16384-byte
	// default.
	inspectContent := nativeInspectContentDerived()
	registerOutputBound("/$defs/worktree_inspect_result/properties/content/maxLength", inspectContent)
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/0/properties/content/maxLength", inspectContent)
	registerOutputBound("/$defs/worktree_inspect_result/oneOf/1/properties/content/maxLength", inspectVacuous)

	// -- worktree_verify_result (concord_work_transition.worktree_verify
	// mutation; the typed whole-result refusal owner is mutationResult) --

	registerOutputBound("/$defs/worktree_verify_result/properties/branch/maxLength", branchBound)
	registerOutputBound("/$defs/worktree_verify_result/properties/path/maxLength", pathBound)
	registerOutputBound("/$defs/worktree_verify_result/properties/lease_id/maxLength", nativeLeaseIDDerived())
	registerOutputBound("/$defs/worktree_verify_result/properties/output/maxLength", verifyOutputBytes)
	registerOutputBound("/$defs/worktree_verify_result/properties/command/maxItems", commandArgs)
	registerOutputBound("/$defs/worktree_verify_result/properties/command/items/maxLength", commandPart)
	// The verify result publishes exactly one fixed changed ref and one fixed
	// next-valid intent whose required_fields list is exactly the two fixed
	// field names; the evaluators read the literal slice shapes in
	// internal/agent/mutations.go:mutateWorktreeVerify and the 1:1 projection
	// helpers, and fail closed if either changes.
	registerOutputBound("/$defs/worktree_verify_result/properties/changed_refs/maxItems", nativeFixedShapeDerived("changed_refs"))
	registerOutputBound("/$defs/worktree_verify_result/properties/next_valid_intents/maxItems", nativeFixedShapeDerived("next_valid_intents"))
	registerOutputBound("/$defs/worktree_verify_result/properties/next_valid_intents/items/properties/required_fields/maxItems", nativeFixedShapeDerived("required_fields"))

	// -- active_worktree_verify_lease (continuity read view) --

	// The continuity view projects the retained lease path straight from
	// SQL: heldWorktreeVerifyLeasesByOwnerTx scans worktree_verify_leases
	// rows and copies the path into the read with no existence check (no
	// os.Stat, no git probe), the column carries no length CHECK in the
	// final DDL, and the only write-side guard is the claim's absoluteness
	// check over a join of the unbounded operator dataDir and the unguarded
	// ambient Project id. Retained paths genuinely project without any
	// admission, store, or readback bound.
	registerOutputBound("/$defs/active_worktree_verify_lease/properties/path/maxLength", unboundedOutput(nativeLeasePathGap, nativeReadSurfaceRefusal))
	registerOutputBound("/$defs/active_worktree_verify_lease/properties/command/maxItems", commandArgs)
	registerOutputBound("/$defs/active_worktree_verify_lease/properties/command/items/maxLength", commandPart)
	registerOutputBound("/$defs/active_worktree_verify_lease/properties/acquired_at/maxLength", nativeAcquiredAtDerived())

	// -- leaf definitions --

	// Every published value of this def is a nativeDigest output (exactly
	// sha256: + 64 hex = 71 characters) or a guarded echo of an admitted
	// request digest of the same shape; the echo half is input-reachable and
	// admits 71 exactly.
	registerOutputBound("/$defs/native_oracle_digest/maxLength", nativeDigestDerived(nativeDigestFile, "nativeDigest"))
	registerOutputBound("/$defs/native_oracle_go_witness/maxLength", testNameBound)
	registerOutputBound("/$defs/native_oracle_package_witness/maxLength", nativePackageWitnessDerived(false))
	registerOutputBound("/$defs/native_oracle_package_witness/x-maxBytes", nativePackageWitnessDerived(false))
	registerOutputBound("/$defs/native_oracle_package_inventory/properties/skipped_tests/maxItems", nativeInventorySkipCount(false))
	registerOutputBound("/$defs/native_oracle_package_inventory/properties/skipped_tests/items/maxLength", goInlineBound("internal/store/native_recipe_case.go", "validateNativeGoManifest", "len(name)", boundBytes))
	registerOutputBound("/$defs/native_oracle_package_inventory/properties/nested_skipped_tests/maxItems", nativeInventorySkipCount(true))
	registerOutputBound("/$defs/native_oracle_package_inventory/properties/nested_skipped_tests/items/maxLength", goInlineBound("internal/store/native_go_events.go", "consume", "len(test)", boundBytes))
	registerOutputBound("/$defs/native_oracle_stream/properties/sha256/maxLength", nativeDigestDerived(nativeDigestFile, "nativeStreamDescriptor"))
	registerOutputBound("/$defs/native_oracle_stream/properties/ref/maxLength", nativePrefixedDigestDerived("oracle_output:"))
	registerOutputBound("/$defs/native_oracle_file/properties/path/maxLength", repoPathBytes)
	registerOutputBound("/$defs/native_oracle_file/properties/blob_oid/maxLength", nativeGitOIDWidthDerived())
	registerOutputBound("/$defs/native_oracle_file/properties/sha256/maxLength", nativeDigestDerived(nativeOracleGoFile, "nativeGitFile"))
	registerOutputBound("/$defs/native_oracle_output_page/properties/run_ref/maxLength", inputBound("/$defs/work_browse_worktree_inspect_input/properties/run_ref/maxLength"))
	registerOutputBound("/$defs/native_oracle_output_page/properties/data_base64/maxLength", dataBase64Bound)
	registerOutputBound("/$defs/native_oracle_stage/properties/name/maxLength", nativeStageNameDerived())
	registerOutputBound("/$defs/native_oracle_stage/properties/argv/maxItems", commandArgs)
	registerOutputBound("/$defs/native_oracle_stage/properties/argv/items/maxLength", commandPart)

	// -- native_oracle_preparation (the dispatched worker packet) and its
	// retained twin (read views: continuity, work pin; typed refusal owner is
	// the shared read refusal) --

	prep := "/$defs/native_oracle_preparation"
	// RunRef echoes worktreeVerifyOperationRef, whose widest live arm is the
	// no-separator fallback over the widest admitted lease id.
	registerOutputBoundWithTwin(prep+"/properties/run_ref/maxLength", nativeRunRefDerived())
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/run_ref/maxLength", nativeRunRefDerived())
	registerOutputBoundWithTwin(prep+"/properties/work_id/maxLength", inputBound("/$defs/id/maxLength"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/work_id/maxLength", inputBound("/$defs/id/maxLength"))
	// The ambient Project identity has no admission guard anywhere
	// (internal/store/operator.go:CreateProjectForProduct checks non-empty
	// only and projects.id carries no length CHECK), and every writer of the
	// published value copies it verbatim; genuinely unbounded, byte-ceiling
	// only.
	for _, branch := range []string{"", "/oneOf/0", "/oneOf/1", "/oneOf/2"} {
		registerRetainedTwinBound(prep+branch+"/properties/project_id/maxLength", unboundedOutput(projectIDGap, nativeReadSurfaceRefusal, nativeMutationResultRefusal))
	}
	registerOutputBoundWithTwin(prep+"/properties/subject_commit/maxLength", shaBound)
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/subject_commit/maxLength", shaBound)
	registerOutputBoundWithTwin(prep+"/properties/bundle_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeBundleDigest"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/bundle_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeBundleDigest"))
	registerOutputBoundWithTwin(prep+"/properties/logical_argv_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeOracleInitialResult"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/logical_argv_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeOracleInitialResult"))
	registerOutputBoundWithTwin(prep+"/properties/manifest_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "nativeGitFile"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/manifest_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "nativeGitFile"))
	registerOutputBoundWithTwin(prep+"/properties/native_plan_sha256/maxLength", nativeDigestDerived(nativeOracleFile, "verifyNativeOracle"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/native_plan_sha256/maxLength", nativeDigestDerived(nativeOracleFile, "verifyNativeOracle"))
	registerOutputBoundWithTwin(prep+"/properties/build_environment_digest/maxLength", maxBound(
		nativeDigestDerived(nativeOracleGoFile, "resolveNativeGoEnvironment"),
		nativeDigestDerived(nativeOracleHostFile, "finalizeNativeTargetEnvironment"),
	))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/build_environment_digest/maxLength", maxBound(
		nativeDigestDerived(nativeOracleGoFile, "resolveNativeGoEnvironment"),
		nativeDigestDerived(nativeOracleHostFile, "finalizeNativeTargetEnvironment"),
	))
	registerOutputBoundWithTwin(prep+"/properties/logical_cwd/maxLength", repoPathBytes)
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/logical_cwd/maxLength", repoPathBytes)
	registerOutputBoundWithTwin(prep+"/properties/manifest_blob/maxLength", shaBound)
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/manifest_blob/maxLength", shaBound)
	registerOutputBoundWithTwin(prep+"/properties/files/maxItems", filesBound)
	registerOutputBoundWithTwin(prep+"/oneOf/0/properties/files/maxItems", filesBound)
	registerOutputBoundWithTwin(prep+"/oneOf/1/properties/files/maxItems", filesBound)
	// No finite per-field guard exists on either family: the target identity
	// concatenates recipe version pins admitted only through the unbounded
	// dotted-numeric pattern, and the Go family concatenates the go.mod
	// toolchain directive admitted only through nativeGoToolchainName, whose
	// digit runs are also unbounded - the width of today's toolchain string is
	// not a source. Byte-ceiling only.
	for _, branch := range []string{"", "/oneOf/0", "/oneOf/1", "/oneOf/2"} {
		registerRetainedTwinBound(prep+branch+"/properties/toolchain_identity/maxLength", unboundedOutput(toolchainGap, nativeReadSurfaceRefusal, nativeMutationResultRefusal))
	}
	// Full literal witnesses are bounded by the explicit distinct-set guard.
	// The root selector's separate byte guard cannot bound nested witnesses.
	registerOutputBoundWithTwin(prep+"/properties/selected_test_names/maxItems", nativeSelectorNamesDerived())
	registerOutputBoundWithTwin(prep+"/oneOf/0/properties/selected_test_names/maxItems", nativeSelectorNamesDerived())
	// Distinct selected witnesses are bounded by the rendered verify-command
	// shape: nativeTargetSelector prepends exactly one runner literal to the
	// witness list and refuses more than maxWorktreeVerifyCommandArgs parts.
	registerOutputBoundWithTwin(prep+"/properties/selected_witness_ids/maxItems", nativeWitnessSelectorDerived())
	registerOutputBoundWithTwin(prep+"/oneOf/1/properties/selected_witness_ids/maxItems", nativeWitnessSelectorDerived())
	registerOutputBoundWithTwin(prep+"/properties/selected_witness_ids/items/maxLength", maxBound(witnessBytes, nativePackageWitnessDerived(false)))
	registerOutputBoundWithTwin(prep+"/oneOf/1/properties/selected_witness_ids/items/maxLength", witnessBytes)
	registerOutputBoundWithTwin(prep+"/properties/case_to_test_map/patternProperties/^case:*/maxItems", caseMapGoBound)
	registerOutputBoundWithTwin(prep+"/oneOf/0/properties/case_to_test_map/patternProperties/^case:*/maxItems", caseMapGoBound)
	registerOutputBoundWithTwin(prep+"/properties/case_to_witness_map/patternProperties/^case:*/maxItems", caseMapTargetBound)
	registerOutputBoundWithTwin(prep+"/oneOf/1/properties/case_to_witness_map/patternProperties/^case:*/maxItems", caseMapTargetBound)
	registerOutputBoundWithTwin(prep+"/properties/case_to_witness_map/patternProperties/^case:*/items/maxLength", maxBound(witnessBytes, nativePackageWitnessDerived(false)))
	registerOutputBoundWithTwin(prep+"/oneOf/1/properties/case_to_witness_map/patternProperties/^case:*/items/maxLength", witnessBytes)
	registerOutputBoundWithTwin(prep+"/oneOf/2/properties/selected_witness_ids/maxItems", nativePackageWitnessDerived(true))
	registerOutputBoundWithTwin(prep+"/oneOf/2/properties/case_to_witness_map/patternProperties/^case:*/maxItems", nativePackageWitnessDerived(true))
	registerOutputBoundWithTwin(prep+"/properties/harness_input_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "collectNativeGoHarnessFiles"))
	registerOutputBoundWithTwin(prep+"/oneOf/*/properties/harness_input_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "collectNativeGoHarnessFiles"))

	// -- native_oracle_result (published inside the worktree_verify mutation
	// result; typed refusal owner is mutationResult) --

	result := "/$defs/native_oracle_result"
	// The patternProperties key carries a literal regex character class, so
	// the exact segment cannot be cited (the ledger pattern validator refuses
	// '[' and ']'); the ^case:* prefix matches the case-key segment without
	// crossing a separator. The validator gap is reported to the parent.
	registerOutputBound(result+"/oneOf/*/properties/run_ref/maxLength", nativeRunRefDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/run_ref/maxLength", nativeRunRefDerived())
	registerOutputBound(result+"/oneOf/*/properties/work_id/maxLength", inputBound("/$defs/id/maxLength"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/work_id/maxLength", inputBound("/$defs/id/maxLength"))
	registerOutputBound(result+"/oneOf/*/properties/project_id/maxLength", unboundedOutput(projectIDGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/project_id/maxLength", unboundedOutput(projectIDGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/properties/subject_commit/maxLength", shaBound)
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/subject_commit/maxLength", shaBound)
	registerOutputBound(result+"/oneOf/*/properties/bundle_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeBundleDigest"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/bundle_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeBundleDigest"))
	registerOutputBound(result+"/oneOf/*/properties/logical_argv_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeOracleInitialResult"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/logical_argv_digest/maxLength", nativeDigestDerived(nativeOracleFile, "nativeOracleInitialResult"))
	registerOutputBound(result+"/oneOf/*/properties/manifest_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "nativeGitFile"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/manifest_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "nativeGitFile"))
	registerOutputBound(result+"/oneOf/*/properties/native_plan_sha256/maxLength", nativeDigestDerived(nativeOracleFile, "verifyNativeOracle"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/native_plan_sha256/maxLength", nativeDigestDerived(nativeOracleFile, "verifyNativeOracle"))
	registerOutputBound(result+"/oneOf/*/properties/build_environment_digest/maxLength", maxBound(
		nativeDigestDerived(nativeOracleGoFile, "resolveNativeGoEnvironment"),
		nativeDigestDerived(nativeOracleHostFile, "finalizeNativeTargetEnvironment"),
	))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/build_environment_digest/maxLength", maxBound(
		nativeDigestDerived(nativeOracleGoFile, "resolveNativeGoEnvironment"),
		nativeDigestDerived(nativeOracleHostFile, "finalizeNativeTargetEnvironment"),
	))
	registerOutputBound(result+"/oneOf/*/properties/logical_cwd/maxLength", repoPathBytes)
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/logical_cwd/maxLength", repoPathBytes)
	registerOutputBound(result+"/oneOf/*/properties/manifest_blob/maxLength", shaBound)
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/manifest_blob/maxLength", shaBound)
	registerOutputBound(result+"/oneOf/*/properties/files/maxItems", filesBound)
	registerOutputBound(result+"/oneOf/*/oneOf/0/properties/files/maxItems", filesBound)
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/files/maxItems", filesBound)
	// Byte-ceiling only: see the preparation rows for the producer argument.
	registerOutputBound(result+"/oneOf/*/properties/toolchain_identity/maxLength", unboundedOutput(toolchainGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/toolchain_identity/maxLength", unboundedOutput(toolchainGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/properties/control_id/maxLength", inputBound("/$defs/native_oracle_request/properties/control_id/maxLength"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/control_id/maxLength", inputBound("/$defs/native_oracle_request/properties/control_id/maxLength"))
	registerOutputBound(result+"/oneOf/*/properties/stages/maxItems", stageBudget)
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/stages/maxItems", stageBudget)
	// Detail carries the failure text: verifyNativeOracle assigns
	// err.Error(), and the result_json fallback chain rewrites Detail to one
	// fixed string only when clearing Output was not enough, so a committed
	// Detail is either the error text below the 48 KiB chain or that fixed
	// string. The chain constants are inline literals, and the 4096-character
	// consumer is below the chain bound.
	registerOutputBound(result+"/oneOf/*/properties/detail/maxLength", unboundedOutput(detailGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/detail/maxLength", unboundedOutput(detailGap, nativeMutationResultRefusal))
	registerOutputBound(result+"/oneOf/*/properties/selected_test_names/maxItems", nativeSelectorNamesDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/selected_test_names/maxItems", nativeSelectorNamesDerived())
	registerOutputBound(result+"/oneOf/*/properties/selected_witness_ids/maxItems", nativeWitnessSelectorDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/selected_witness_ids/maxItems", nativeWitnessSelectorDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/2/properties/selected_witness_ids/maxItems", nativePackageWitnessDerived(true))
	registerOutputBound(result+"/oneOf/*/properties/selected_witness_ids/items/maxLength", maxBound(witnessBytes, nativePackageWitnessDerived(false)))
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/selected_witness_ids/items/maxLength", witnessBytes)
	registerOutputBound(result+"/oneOf/*/properties/case_to_test_map/patternProperties/^case:*/maxItems", caseMapGoBound)
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/case_to_test_map/patternProperties/^case:*/maxItems", caseMapGoBound)
	registerOutputBound(result+"/oneOf/*/properties/case_to_witness_map/patternProperties/^case:*/maxItems", caseMapTargetBound)
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/case_to_witness_map/patternProperties/^case:*/maxItems", caseMapTargetBound)
	registerOutputBound(result+"/oneOf/*/oneOf/2/properties/case_to_witness_map/patternProperties/^case:*/maxItems", nativePackageWitnessDerived(true))
	registerOutputBound(result+"/oneOf/*/properties/case_to_witness_map/patternProperties/^case:*/items/maxLength", maxBound(witnessBytes, nativePackageWitnessDerived(false)))
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/case_to_witness_map/patternProperties/^case:*/items/maxLength", witnessBytes)
	registerOutputBound(result+"/oneOf/*/properties/harness_input_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "collectNativeGoHarnessFiles"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/harness_input_digest/maxLength", nativeDigestDerived(nativeOracleGoFile, "collectNativeGoHarnessFiles"))
	registerOutputBound(result+"/oneOf/1/properties/preparation_run_ref/maxLength", inputBound("/$defs/native_oracle_request/oneOf/1/properties/preparation_run_ref/maxLength"))
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/preparation_run_ref/maxLength", inputBound("/$defs/native_oracle_request/oneOf/1/properties/preparation_run_ref/maxLength"))
	// Observed populations join against the selected sets: the Go family
	// assigns slices.Clone(result.SelectedTestNames) in consumeStage, and the
	// target family refuses unless observed
	// witnesses equal the selected population
	// (worktree_verify_oracle_host_run.go), so both inherit the selector
	// bounds.
	registerOutputBound(result+"/oneOf/1/properties/observed_test_names/maxItems", nativeObservedNamesDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/*/properties/observed_test_names/maxItems", nativeObservedNamesDerived())
	registerOutputBound(result+"/oneOf/1/properties/observed_witness_ids/maxItems", nativeObservedWitnessDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/observed_witness_ids/maxItems", nativeObservedWitnessDerived())
	registerOutputBound(result+"/oneOf/*/oneOf/2/properties/observed_witness_ids/maxItems", nativePackageObservedDerived())
	registerOutputBound(result+"/oneOf/1/properties/observed_witness_ids/items/maxLength", maxBound(witnessBytes, nativePackageWitnessDerived(false)))
	registerOutputBound(result+"/oneOf/*/oneOf/1/properties/observed_witness_ids/items/maxLength", witnessBytes)
}

const (
	// nativeReadSurfaceRefusal is the typed whole-result refusal owner for
	// every read-published native surface: continuity, work-pin, and
	// worker-packet views of preparations, the worktree inspect read, and the
	// continuity view of held verify leases.
	nativeReadSurfaceRefusal = "internal/agent/runtime.go:readSizeRefusal"
	// nativeMutationResultRefusal is the typed whole-result refusal owner for
	// the worktree_verify mutation result and the native_oracle_result it
	// embeds: mutationResult emits the limit_exceeded envelope refusal and
	// validates inline and replayed mutation success alike.
	nativeMutationResultRefusal = "internal/agent/mutations.go:mutationResult"

	nativeDigestFile     = "internal/store/worktree_verify_output.go"
	nativeWorktreesFile  = "internal/store/worktrees.go"
	nativeOracleFile     = "internal/store/worktree_verify_oracle.go"
	nativeOracleGoFile   = "internal/store/worktree_verify_oracle_go.go"
	nativeOracleHostFile = "internal/store/worktree_verify_oracle_host.go"
	nativeOracleHostPlan = "internal/store/worktree_verify_oracle_host_plan.go"
	nativeOracleHostRun  = "internal/store/worktree_verify_oracle_host_run.go"
	nativeProjectLocator = "internal/store/project_locator.go"
	nativeMutationsFile  = "internal/agent/mutations.go"
	nativeAuthorityFile  = "internal/agent/authority.go"
	nativeWorkerCtrlFile = "internal/agent/worker_control.go"
)

// gap texts shared by repeated registrations; each names the exact producing
// symbol and the missing proof mechanism.

const projectIDGap = "the ambient Project identity has no admission guard: internal/store/operator.go:CreateProjectForProduct checks non-empty only and projects.id carries no length CHECK, and every native writer (nativeOracleInitialResult and the read views) copies the session's ambient Project id verbatim, so host Project IDs are genuinely unbounded and are not made finite by today's short ids"

const nativeLeasePathGap = "the continuity view projects the retained worktree_verify_leases.path straight from SQL (internal/store/worktrees.go:heldWorktreeVerifyLeasesByOwnerTx scans held rows and copies the path verbatim) with no existence check, no os.Stat, and no git probe; the column carries no length CHECK in the final DDL, and the only write-side guard is the claim's filepath.IsAbs check over filepath.Join(dataDir, \"worktrees\", projectID, workID) with the unbounded operator dataDir and the unguarded ambient Project id, so retained paths project without any admission, store, or readback bound"

const toolchainGap = "no per-field producer guard exists on either family: worktree_verify_oracle_host.go:nativeTargetEnvironment composes RuntimeIdentity from recipe version pins admitted only through the unbounded nativeTargetVersionPin dotted-numeric pattern, and the Go family composes the go.mod toolchain directive validated only by worktree_verify_oracle_go.go:nativePinnedGoToolchain through nativeGoToolchainName, whose digit runs are also unbounded; no truncation or length guard exists anywhere in the writer chain, so the value is byte-ceiling only"

const detailGap = "internal/store/worktree_verify_oracle.go:verifyNativeOracle assigns result.Oracle.Detail = err.Error() with no field guard: worktree_verify_output.go:marshalNativeVerifyRecord rewrites Detail to one fixed string only when clearing Output did not fit the 48 KiB result_json chain, so failure text accumulates to that aggregate without any per-field bound; a committed Detail beyond the 4096-character consumer is refused whole at the envelope"

// ---------------------------------------------------------------------------
// Family-local derived evaluators
//
// Each evaluator inspects the exact live AST of the named writer and fails
// closed when the derivation changes shape. None of them trusts a copied
// number: widths come from the live guards, constants, grammars, or layout.
// ---------------------------------------------------------------------------

// nativeFuncDecl loads one function declared exactly once with a body.
func nativeFuncDecl(rt *ledgerRuntime, file, name string) (*ast.FuncDecl, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return nil, err
	}
	fns := ledgerFuncDeclsNamed(goS.file, name)
	if len(fns) == 0 {
		return nil, fmt.Errorf("native bound: %s is not declared in %s", name, file)
	}
	if len(fns) > 1 || fns[0].Body == nil {
		return nil, fmt.Errorf("native bound: %s is ambiguous or bodyless in %s", name, file)
	}
	return fns[0], nil
}

// nativeMethodDecl keeps an extracted proof tied to its exact receiver owner.
func nativeMethodDecl(rt *ledgerRuntime, file, receiver, name string) (*ast.FuncDecl, error) {
	fn, err := nativeFuncDecl(rt, file, name)
	if err != nil {
		return nil, err
	}
	if fn.Recv == nil || len(fn.Recv.List) != 1 || ledgerExprText(fn.Recv.List[0].Type) != "*"+receiver {
		return nil, fmt.Errorf("native bound: %s is not owned by *%s in %s", name, receiver, file)
	}
	return fn, nil
}

func nativeUnavailableReturn(block *ast.BlockStmt) bool {
	if block == nil || len(block.List) != 1 {
		return false
	}
	ret, ok := block.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	return ok && ledgerCalleeMatches(call, "nativeUnavailable")
}

// nativeGoFile loads a parsed Go source.
func nativeGoFile(rt *ledgerRuntime, file string) (*goSource, error) {
	return rt.loadGo(file)
}

// nativeConstValue resolves one package-level integer constant in file.
func nativeConstValue(rt *ledgerRuntime, file, symbol string) (int64, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return 0, err
	}
	return rt.evalNumeric(goS, nil, &ast.Ident{Name: symbol})
}

// nativeStringLit unquotes one string BasicLit.
func nativeStringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// nativeAnyStringLit unquotes a node when it is a string BasicLit.
func nativeAnyStringLit(n ast.Node) (string, bool) {
	if e, ok := n.(ast.Expr); ok {
		return nativeStringLit(e)
	}
	return "", false
}

// nativeIntLit reads one integer BasicLit. Go literals are C-style, so the
// base comes from the literal's own prefix.
func nativeIntLit(e ast.Expr) (int64, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseInt(lit.Value, 0, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// nativeCalls collects every call whose resolved callee name matches.
func nativeCalls(node ast.Node, callee string) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, callee) {
			found = append(found, call)
		}
		return true
	})
	return found
}

// nativeASCIIWidth emits equal byte and character constraints for a value
// whose writer alphabet is pure ASCII (hex digits, fixed literals, git
// object names, RFC3339 timestamps, literal stage names).
func nativeASCIIWidth(chars int64, describe string) evaluatedBound {
	return evaluatedBound{
		kind:     "derived",
		describe: describe,
		constraints: []boundConstraint{
			{unit: boundCharacters, value: chars, from: describe},
			{unit: boundBytes, value: chars, from: describe + " (ASCII)"},
		},
	}
}

// nativeCharsWidth emits one character constraint for values whose writer is
// not provably byte-per-character.
func nativeCharsWidth(chars int64, describe string) evaluatedBound {
	return evaluatedBound{
		kind:        "derived",
		describe:    describe,
		constraints: []boundConstraint{{unit: boundCharacters, value: chars, from: describe}},
	}
}

// nativeItemsWidth emits one item constraint.
func nativeItemsWidth(items int64, describe string) evaluatedBound {
	return evaluatedBound{
		kind:        "derived",
		describe:    describe,
		constraints: []boundConstraint{{unit: boundItems, value: items, from: describe}},
	}
}

// Select an enforcing upper comparison, not equality checks on a narrower
// family arm. Every disjunct selected here independently enters the refusal.
func nativeRejectUpperBound(file, function, measurement string, unit boundUnit) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, _ *boundCensus) (evaluatedBound, error) {
		fn, err := nativeFuncDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		source, err := nativeGoFile(rt, file)
		if err != nil {
			return evaluatedBound{}, err
		}
		var limits []int64
		var inspect func(ast.Expr)
		inspect = func(expr ast.Expr) {
			if paren, ok := expr.(*ast.ParenExpr); ok {
				inspect(paren.X)
				return
			}
			cmp, ok := expr.(*ast.BinaryExpr)
			if !ok {
				return
			}
			if cmp.Op == token.LOR {
				inspect(cmp.X)
				inspect(cmp.Y)
				return
			}
			if (cmp.Op == token.GTR || cmp.Op == token.GEQ) && ledgerExprText(cmp.X) == measurement {
				if value, err := rt.evalNumeric(source, fn, cmp.Y); err == nil {
					if cmp.Op == token.GEQ {
						value--
					}
					limits = append(limits, value)
				}
			}
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			branch, ok := n.(*ast.IfStmt)
			if !ok || len(branch.Body.List) != 1 {
				return true
			}
			ret, ok := branch.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}
			refuses := len(ret.Results) == 1 && ledgerExprText(ret.Results[0]) == "false"
			if call, ok := ret.Results[len(ret.Results)-1].(*ast.CallExpr); ok {
				refuses = ledgerCalleeMatches(call, "nativeUnavailable") || ledgerCalleeMatches(call, "oracleFailure")
			}
			if refuses {
				inspect(branch.Cond)
			}
			return true
		})
		if len(limits) != 1 {
			return evaluatedBound{}, fmt.Errorf("native bound: %s:%s has %d enforcing upper refusals for %s", file, function, len(limits), measurement)
		}
		describe := fmt.Sprintf("enforcing upper refusal %s:%s on %s", file, function, measurement)
		return evaluatedBound{kind: "derived", describe: describe, constraints: []boundConstraint{{unit: unit, value: limits[0], from: describe}}}, nil
	}}
}

func nativeInventorySkipCount(nested bool) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		consume, err := nativeMethodDecl(rt, "internal/store/native_go_events.go", "nativeGoEventGrade", "consume")
		if err != nil {
			return evaluatedBound{}, err
		}
		grade, err := nativeFuncDecl(rt, "internal/store/native_go_package.go", "nativeGoPackageGrade")
		if err != nil {
			return evaluatedBound{}, err
		}
		field, output := "g.skipped", "inventory.SkippedTests"
		guard := nativeRejectUpperBound("internal/store/native_recipe_case.go", "validateNativeGoManifest", "len(m.ExpectedSkips)", boundItems)
		if nested {
			field, output = "g.nestedSkips", "inventory.NestedSkippedTests"
			guard = nativeRejectUpperBound("internal/store/native_go_events.go", "consume", "len(g.nestedSkips)", boundItems)
		}
		appended, copied, membershipRefusal, terminalRefusal, terminalState := false, false, false, false, false
		ast.Inspect(consume, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				appended = appended || ledgerExprText(assign.Lhs[0]) == field && ledgerExprText(assign.Rhs[0]) == "append("+field+", test)"
				terminalState = terminalState || ledgerExprText(assign.Lhs[0]) == "g.state[test]" && ledgerExprText(assign.Rhs[0]) == "3"
			}
			if branch, ok := n.(*ast.IfStmt); ok && ledgerExprText(branch.Cond) == "!nested && (!g.packageMode || !slices.Contains(g.expected, test))" {
				membershipRefusal = len(nativeCalls(branch, "nativeUnavailable")) == 1
			}
			if branch, ok := n.(*ast.IfStmt); ok && ledgerExprText(branch.Cond) == "!known || phase != 1 || nested && rootPhase != 1" {
				terminalRefusal = len(nativeCalls(branch, "nativeUnavailable")) == 1
			}
			return true
		})
		ast.Inspect(grade, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				copied = copied || ledgerExprText(assign.Lhs[0]) == output && ledgerExprText(assign.Rhs[0]) == "slices.Clone("+field+")"
			}
			return true
		})
		if !appended || !copied || !terminalRefusal || !terminalState || !nested && !membershipRefusal {
			return evaluatedBound{}, fmt.Errorf("native bound: package skip population no longer joins the guarded event list")
		}
		bound, err := rt.evaluateSource(guard, census)
		if err != nil {
			return evaluatedBound{}, err
		}
		if nested {
			// The >= guard observes the pre-append length; the one admitted
			// append adds one to the published maximum.
			for i := range bound.constraints {
				bound.constraints[i].value++
			}
		}
		return bound, nil
	}}
}

// A package case admits only the one literal prefix plus the validated cwd.
// Per-case uniqueness and the distinct-set return make both populations one.
func nativePackageWitnessDerived(cardinality bool) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		fn, err := nativeFuncDecl(rt, nativeOracleGoFile, "nativeOracleCaseTests")
		if err != nil {
			return evaluatedBound{}, err
		}
		prefix := ""
		validRefusal, uniqueRefusal := false, false
		ast.Inspect(fn, func(n ast.Node) bool {
			branch, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			if ledgerExprText(branch.Cond) == "!valid || local[name]" {
				for _, stmt := range branch.Body.List {
					if ret, ok := stmt.(*ast.ReturnStmt); ok && len(ret.Results) == 2 && ledgerExprText(ret.Results[0]) == "nil" {
						if call, ok := ret.Results[1].(*ast.CallExpr); ok && ledgerCalleeMatches(call, "nativeUnavailable") {
							validRefusal, uniqueRefusal = true, true
						}
					}
				}
			}
			if ledgerExprText(branch.Cond) != "m.Kind == nativeGoPackageKind" || len(branch.Body.List) != 1 {
				return true
			}
			assign, ok := branch.Body.List[0].(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || ledgerExprText(assign.Lhs[0]) != "valid" {
				return true
			}
			eq, ok := assign.Rhs[0].(*ast.BinaryExpr)
			if !ok || eq.Op != token.EQL || ledgerExprText(eq.X) != "name" {
				return true
			}
			concat, ok := eq.Y.(*ast.BinaryExpr)
			if ok && concat.Op == token.ADD && ledgerExprText(concat.Y) == "m.PackageCwd" {
				prefix, _ = nativeStringLit(concat.X)
			}
			return true
		})
		if prefix == "" || !validRefusal || !uniqueRefusal {
			return evaluatedBound{}, fmt.Errorf("native bound: package witnesses no longer refuse nonliteral or duplicate case entries")
		}
		if _, _, err := nativeSelectorNameCount(rt); err != nil {
			return evaluatedBound{}, err
		}
		if cardinality {
			return nativeItemsWidth(1, "one distinct package witness from literal-prefix/cwd equality and duplicate refusal"), nil
		}
		manifest, err := nativeFuncDecl(rt, "internal/store/native_recipe_case.go", "validateNativeGoManifest")
		cwdValidated := false
		if err == nil {
			for _, call := range nativeCalls(manifest, "validateWorkContextRepoPath") {
				cwdValidated = cwdValidated || len(call.Args) == 1 && ledgerExprText(call.Args[0]) == "m.PackageCwd"
			}
		}
		if !cwdValidated {
			return evaluatedBound{}, fmt.Errorf("native bound: package cwd no longer passes repository path admission: %v", err)
		}
		cwd, err := rt.evaluateSource(goInlineBound("internal/store/work_context.go", "validateWorkContextRepoPath", "len(path)", boundBytes), census)
		if err != nil {
			return evaluatedBound{}, err
		}
		for _, c := range cwd.constraints {
			if c.unit == boundBytes {
				width := c.value + int64(len(prefix))
				return evaluatedBound{kind: "derived", describe: "literal package prefix plus admitted repository cwd", constraints: []boundConstraint{{unit: boundBytes, value: width, from: cwd.describe}}}, nil
			}
		}
		return evaluatedBound{}, fmt.Errorf("native bound: package cwd has no byte bound")
	}}
}

func nativePackageObservedDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		fn, err := nativeMethodDecl(rt, nativeOracleGoFile, "nativeGoOracleRun", "consumeStage")
		if err != nil {
			return evaluatedBound{}, err
		}
		joined := false
		ast.Inspect(fn, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 && ledgerExprText(assign.Lhs[0]) == "result.ObservedWitnessIDs" && ledgerExprText(assign.Rhs[0]) == "slices.Clone(result.SelectedWitnessIDs)" {
				joined = true
			}
			return true
		})
		if !joined {
			return evaluatedBound{}, fmt.Errorf("native bound: package observed witnesses no longer clone the selected set")
		}
		return rt.evaluateSource(nativePackageWitnessDerived(true), census)
	}}
}

// nativeDigestShape verifies one sha256-prefixed digest function: exactly one
// sha256.Sum256 call, exactly one hex.EncodeToString call, and the sole
// string literal "sha256:". Its output is therefore exactly 71 ASCII
// characters: the 7-byte prefix plus the 64 hex digits of the 32-byte sum.
func nativeDigestShape(rt *ledgerRuntime, file, function string) (int64, error) {
	fn, err := nativeFuncDecl(rt, file, function)
	if err != nil {
		return 0, err
	}
	sums := nativeCalls(fn, "sha256.Sum256")
	hexes := nativeCalls(fn, "hex.EncodeToString")
	var literals []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if lit, ok := nativeAnyStringLit(n); ok {
			literals = append(literals, lit)
		}
		return true
	})
	if len(sums) != 1 || len(hexes) != 1 || len(literals) != 1 || literals[0] != "sha256:" {
		return 0, fmt.Errorf("native bound: %s:%s no longer has the fixed sha256-prefixed digest body", file, function)
	}
	return int64(len("sha256:")) + 2*32, nil
}

// nativeDigestDerived closes a row whose writer composes the published value
// through nativeDigest. When the composer is nativeDigest itself the shape
// check is the whole proof; otherwise the composer must still route through
// at least one nativeDigest call.
func nativeDigestDerived(composerFile, composerFunction string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		width, err := nativeDigestShape(rt, nativeDigestFile, "nativeDigest")
		if err != nil {
			return evaluatedBound{}, err
		}
		if composerFile != nativeDigestFile || composerFunction != "nativeDigest" {
			fn, err := nativeFuncDecl(rt, composerFile, composerFunction)
			if err != nil {
				return evaluatedBound{}, err
			}
			if len(nativeCalls(fn, "nativeDigest")) == 0 {
				return evaluatedBound{}, fmt.Errorf("native bound: %s:%s no longer composes the published value through nativeDigest", composerFile, composerFunction)
			}
		}
		return nativeASCIIWidth(width, fmt.Sprintf("derived %s:%s through nativeDigest (%d characters)", composerFile, composerFunction, width)), nil
	}}
}

// nativePrefixedDigestDerived closes the stream ref row: nativeStreamDescriptor
// must assign Ref = <literal> + nativeDigest(...), so the width is the
// literal plus the 71-character digest.
func nativePrefixedDigestDerived(literal string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		width, err := nativeDigestShape(rt, nativeDigestFile, "nativeDigest")
		if err != nil {
			return evaluatedBound{}, err
		}
		fn, err := nativeFuncDecl(rt, nativeDigestFile, "nativeStreamDescriptor")
		if err != nil {
			return evaluatedBound{}, err
		}
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || be.Op != token.ADD {
				return true
			}
			lit, ok := nativeStringLit(be.X)
			if !ok || lit != literal {
				return true
			}
			if call, ok := be.Y.(*ast.CallExpr); ok && ledgerCalleeMatches(call, "nativeDigest") {
				found = true
			}
			return true
		})
		if !found {
			return evaluatedBound{}, fmt.Errorf("native bound: nativeStreamDescriptor no longer assigns Ref as %q + nativeDigest(...)", literal)
		}
		total := int64(len(literal)) + width
		return nativeASCIIWidth(total, fmt.Sprintf("derived nativeStreamDescriptor Ref = %q + nativeDigest (%d characters)", literal, total)), nil
	}}
}

// nativeAddOperands flattens a string concatenation chain.
func nativeAddOperands(e ast.Expr) []ast.Expr {
	if be, ok := e.(*ast.BinaryExpr); ok && be.Op == token.ADD {
		return append(nativeAddOperands(be.X), nativeAddOperands(be.Y)...)
	}
	return []ast.Expr{e}
}

// nativeSelectorName reports the trailing member of a selector expression.
func nativeSelectorName(e ast.Expr) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", false
	}
	return sel.Sel.Name, true
}

// nativeLeaseIdentity verifies the native lease-identity derivation and
// returns the widest admitted lease id and operation reference in characters.
//
// Writers of req.LeaseID, both traced:
//  1. internal/agent/mutations.go:mutateWorktreeVerify mints
//     sha256Hex(leaseInput) + ":worktree-verify:" + in.WorkID, where
//     sha256Hex is the same sha256-prefixed digest form (71 characters) and
//     in.WorkID is admitted through the payload id def.
//  2. internal/agent/worker_control.go:verifyWorkerControlAssertionTx admits
//     the replayed assertion lease id among the bounded(1..128) identities.
//
// internal/store/worktrees.go:worktreeVerifyOperationRef then renders
// "worktree_verify:" + the Cut prefix, or the whole lease id when the
// separator is absent; the fallback arm is live for writer 2 and is the
// widest published form.
func nativeLeaseIdentity(rt *ledgerRuntime, census *boundCensus) (leaseIDChars, runRefChars int64, err error) {
	digestWidth, err := nativeDigestShape(rt, nativeAuthorityFile, "sha256Hex")
	if err != nil {
		return 0, 0, err
	}
	opRef, err := nativeFuncDecl(rt, nativeWorktreesFile, "worktreeVerifyOperationRef")
	if err != nil {
		return 0, 0, err
	}
	cuts := nativeCalls(opRef, "strings.Cut")
	if len(cuts) != 1 || len(cuts[0].Args) != 2 {
		return 0, 0, fmt.Errorf("native bound: worktreeVerifyOperationRef no longer cuts the lease id exactly once")
	}
	separator, ok := nativeStringLit(cuts[0].Args[1])
	if !ok {
		return 0, 0, fmt.Errorf("native bound: worktreeVerifyOperationRef cut separator is not a literal")
	}
	var prefixes []string
	returnCount := 0
	ast.Inspect(opRef, func(n ast.Node) bool {
		ret, isReturn := n.(*ast.ReturnStmt)
		if !isReturn || len(ret.Results) != 1 {
			return true
		}
		returnCount++
		if be, ok := ret.Results[0].(*ast.BinaryExpr); ok && be.Op == token.ADD {
			if lit, ok := nativeStringLit(be.X); ok {
				prefixes = append(prefixes, lit)
			}
		}
		return true
	})
	if returnCount != 2 || len(prefixes) != 2 || prefixes[0] != prefixes[1] {
		return 0, 0, fmt.Errorf("native bound: worktreeVerifyOperationRef no longer renders both arms with one fixed prefix")
	}
	prefix := prefixes[0]
	mint, err := nativeFuncDecl(rt, nativeMutationsFile, "mutateWorktreeVerify")
	if err != nil {
		return 0, 0, err
	}
	mintFound, mintSites := false, 0
	// The digest operand may arrive inline or through a variable the same
	// function defines as sha256Hex(...) (the live shape: leaseDigest :=
	// sha256Hex(leaseInput); leaseID := leaseDigest + separator + in.WorkID).
	digestDefined := map[string]bool{}
	ast.Inspect(mint, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "sha256Hex") || len(call.Args) != 1 {
			return true
		}
		digestDefined[lhs.Name] = true
		return true
	})
	ast.Inspect(mint, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != "leaseID" {
			return true
		}
		mintSites++
		hasSeparator, hasDigest, hasWorkID := false, false, false
		for _, operand := range nativeAddOperands(assign.Rhs[0]) {
			if lit, ok := nativeStringLit(operand); ok && lit == separator {
				hasSeparator = true
			}
			if len(nativeCalls(operand, "sha256Hex")) > 0 {
				hasDigest = true
			}
			if ident, ok := operand.(*ast.Ident); ok && digestDefined[ident.Name] {
				hasDigest = true
			}
			if name, ok := nativeSelectorName(operand); ok && name == "WorkID" {
				hasWorkID = true
			}
		}
		if hasSeparator && hasDigest && hasWorkID {
			mintFound = true
		}
		return true
	})
	if !mintFound || mintSites != 1 {
		return 0, 0, fmt.Errorf("native bound: mutateWorktreeVerify no longer mints leaseID exactly once as sha256Hex(...) + %q + <input>.WorkID", separator)
	}
	workIDEvaluated, err := rt.evaluateSource(inputBound("/$defs/id/maxLength"), census)
	if err != nil {
		return 0, 0, fmt.Errorf("native bound: the minted work id admission: %w", err)
	}
	workIDChars, ok := workIDEvaluated.capacity(boundCharacters)
	if !ok {
		return 0, 0, fmt.Errorf("native bound: the payload id def has no character capacity")
	}
	replay, err := nativeFuncDecl(rt, nativeWorkerCtrlFile, "verifyWorkerControlAssertionTx")
	if err != nil {
		return 0, 0, err
	}
	replayMax, replayFound := int64(0), false
	ast.Inspect(replay, func(n ast.Node) bool {
		rangeStmt, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		lit, ok := rangeStmt.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		hasLeaseID := false
		for _, elt := range lit.Elts {
			if name, ok := nativeSelectorName(elt); ok && name == "LeaseID" {
				hasLeaseID = true
			}
		}
		if !hasLeaseID {
			return true
		}
		for _, boundedCall := range nativeCalls(rangeStmt.Body, "bounded") {
			if len(boundedCall.Args) != 3 {
				continue
			}
			goS, err := nativeGoFile(rt, nativeWorkerCtrlFile)
			if err != nil {
				continue
			}
			if upper, err := rt.evalNumeric(goS, replay, boundedCall.Args[2]); err == nil {
				replayMax, replayFound = upper, true
			}
		}
		return true
	})
	if !replayFound {
		return 0, 0, fmt.Errorf("native bound: the worker control assertion no longer bounds its lease id identity")
	}
	leaseIDChars = digestWidth + int64(len(separator)) + workIDChars
	if replayMax > leaseIDChars {
		leaseIDChars = replayMax
	}
	runRefChars = int64(len(prefix)) + leaseIDChars
	return leaseIDChars, runRefChars, nil
}

// nativeLeaseIDDerived closes the worktree_verify_result lease_id row.
func nativeLeaseIDDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		leaseIDChars, _, err := nativeLeaseIdentity(rt, census)
		if err != nil {
			return evaluatedBound{}, err
		}
		return nativeCharsWidth(leaseIDChars, fmt.Sprintf("derived widest admitted native lease id: mint arm sha256Hex + separator + id admission, replay assertion admission below it (%d characters)", leaseIDChars)), nil
	}}
}

// nativeRunRefDerived closes the preparation and result run_ref rows.
func nativeRunRefDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, runRefChars, err := nativeLeaseIdentity(rt, census)
		if err != nil {
			return evaluatedBound{}, err
		}
		return nativeCharsWidth(runRefChars, fmt.Sprintf("derived worktreeVerifyOperationRef widest arm: fixed prefix plus the widest admitted lease id (%d characters)", runRefChars)), nil
	}}
}

// nativeNeverAssignedDerived proves the oracle_output arm of
// readNativeVerifyOutput never assigns Branch, Path, or Content: the
// published value is the empty string. The evaluator fails closed the moment
// any of the three members is assigned or keyed.
func nativeNeverAssignedDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		fn, err := nativeFuncDecl(rt, nativeDigestFile, "readNativeVerifyOutput")
		if err != nil {
			return evaluatedBound{}, err
		}
		vacuous := map[string]bool{"Branch": true, "Path": true, "Content": true}
		assigned := ""
		ast.Inspect(fn, func(n ast.Node) bool {
			if assigned != "" {
				return false
			}
			switch e := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range e.Lhs {
					if name, ok := nativeSelectorName(lhs); ok && vacuous[name] {
						assigned = name
					}
				}
			case *ast.KeyValueExpr:
				if ident, ok := e.Key.(*ast.Ident); ok && vacuous[ident.Name] {
					assigned = ident.Name
				}
			}
			return assigned == ""
		})
		if assigned != "" {
			return evaluatedBound{}, fmt.Errorf("native bound: readNativeVerifyOutput now assigns or keys the oracle_output arm's %s member; the never-assigned derivation no longer holds", assigned)
		}
		return evaluatedBound{
			kind:     "derived",
			describe: "derived readNativeVerifyOutput never assigns Branch, Path, or Content on the oracle_output arm (published value is the empty string)",
			constraints: []boundConstraint{
				{unit: boundCharacters, value: 0, from: "derived never-assigned oracle_output members"},
				{unit: boundBytes, value: 0, from: "derived never-assigned oracle_output members"},
			},
		}, nil
	}}
}

// nativeStageNameDerived proves published stage names are the closed string
// literal sets the two plan builders compose: every Name key inside
// normalizeNativeGoOraclePlan and the target stages builder must stay a
// string literal, and the bound is the longest live literal. A computed name
// changes the shape and fails closed.
func nativeStageNameDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		builders := []struct{ file, function string }{
			{nativeOracleGoFile, "normalizeNativeGoOraclePlan"},
			{nativeOracleHostPlan, "stages"},
		}
		longest := 0
		for _, builder := range builders {
			fn, err := nativeFuncDecl(rt, builder.file, builder.function)
			if err != nil {
				return evaluatedBound{}, err
			}
			namesFound := false
			ast.Inspect(fn, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				if ident, ok := kv.Key.(*ast.Ident); !ok || ident.Name != "Name" {
					return true
				}
				lit, ok := nativeStringLit(kv.Value)
				if !ok {
					namesFound = false
					return false
				}
				namesFound = true
				if len(lit) > longest {
					longest = len(lit)
				}
				return true
			})
			if !namesFound {
				return evaluatedBound{}, fmt.Errorf("native bound: %s:%s carries a computed or missing stage Name literal", builder.file, builder.function)
			}
		}
		if longest == 0 {
			return evaluatedBound{}, fmt.Errorf("native bound: no stage name literals found in the plan builders")
		}
		return nativeASCIIWidth(int64(longest), fmt.Sprintf("derived longest stage name in the closed plan-builder literal sets (%d characters)", longest)), nil
	}}
}

// nativeGitOIDWidthDerived proves every published blob_oid is a git object
// identity the snapshot verifier re-derived: validateNativeSnapshotBlob's
// expression switch must still admit exactly the 40- and 64-hex widths, and
// must still refuse when the recomputed identity differs from the tree's.
func nativeGitOIDWidthDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		fn, err := nativeFuncDecl(rt, nativeOracleGoFile, "validateNativeSnapshotBlob")
		if err != nil {
			return evaluatedBound{}, err
		}
		widths := map[int64]bool{}
		switchFound, defaultFound, equalityFound := false, false, false
		ast.Inspect(fn, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SwitchStmt:
				call, ok := e.Tag.(*ast.CallExpr)
				if !ok || !ledgerCalleeMatches(call, "len") || len(call.Args) != 1 {
					return true
				}
				if ledgerExprText(call.Args[0]) != "wantOID" {
					return true
				}
				switchFound = true
				for _, stmt := range e.Body.List {
					clause, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					if clause.List == nil {
						defaultFound = nativeUnavailableReturn(&ast.BlockStmt{List: clause.Body})
						continue
					}
					for _, expr := range clause.List {
						if value, ok := nativeIntLit(expr); ok {
							hash := map[int64]string{40: "sha1.Sum", 64: "sha256.Sum256"}[value]
							hashes := nativeCalls(clause, hash)
							formats := nativeCalls(clause, "fmt.Sprintf")
							widths[value] = hash != "" && len(hashes) == 1 && len(hashes[0].Args) == 1 && ledgerExprText(hashes[0].Args[0]) == "object" && len(formats) == 1 && len(formats[0].Args) == 2 && ledgerExprText(formats[0].Args[0]) == `"%x"` && ledgerExprText(formats[0].Args[1]) == "sum"
						}
					}
				}
			case *ast.IfStmt:
				if ledgerExprText(e.Cond) == "oid != wantOID" && nativeUnavailableReturn(e.Body) {
					equalityFound = true
				}
			}
			return true
		})
		if !switchFound || !defaultFound || !equalityFound || len(widths) != 2 || !widths[40] || !widths[64] {
			return evaluatedBound{}, fmt.Errorf("native bound: validateNativeSnapshotBlob no longer re-derives git object identities at exactly the 40- and 64-hex widths with a refuse-on-mismatch equality")
		}
		return nativeASCIIWidth(64, "derived git object identity width: the snapshot verifier admits exactly 40- and 64-hex identities and refuses any mismatch"), nil
	}}
}

func TestOutputBoundExtractedNativeProofBites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, file, from, to string
		source               outputBoundSource
	}{
		{"blob width", nativeOracleGoFile, "case 40:", "case 41:", nativeGitOIDWidthDerived()},
		{"blob SHA1", nativeOracleGoFile, "sha1.Sum(object)", "sha1.Sum(other)", nativeGitOIDWidthDerived()},
		{"blob SHA256", nativeOracleGoFile, "sha256.Sum256(object)", "sha256.Sum256(other)", nativeGitOIDWidthDerived()},
		{"blob equality", nativeOracleGoFile, "if oid != wantOID {", "if oid != otherOID {", nativeGitOIDWidthDerived()},
		{"blob mismatch refusal", nativeOracleGoFile, `return nativeUnavailable("snapshot blob differs from subject: " + name)`, "return nil", nativeGitOIDWidthDerived()},
		{"blob unsupported width refusal", nativeOracleGoFile, `return nativeUnavailable("unsupported Git object identity")`, "return nil", nativeGitOIDWidthDerived()},
		{"Go selected input", nativeOracleGoFile, "result.ObservedTestNames = slices.Clone(result.SelectedTestNames)", "result.ObservedTestNames = slices.Clone(other.SelectedTestNames)", nativeObservedNamesDerived()},
		{"Go observed output", nativeOracleGoFile, "result.ObservedTestNames = slices.Clone(result.SelectedTestNames)", "other.ObservedTestNames = slices.Clone(result.SelectedTestNames)", nativeObservedNamesDerived()},
		{"Go selected clone", nativeOracleGoFile, "result.ObservedTestNames = slices.Clone(result.SelectedTestNames)", "result.ObservedTestNames = slices.Compact(result.SelectedTestNames)", nativeObservedNamesDerived()},
		{"Go exact method owner", nativeOracleGoFile, "(n *nativeGoOracleRun) consumeStage", "(n *otherRun) consumeStage", nativeObservedNamesDerived()},
		{"Go witness count guard", nativeOracleGoFile, "if len(set) > nativeGoSelectedWitnessLimit {", "if len(other) > nativeGoSelectedWitnessLimit {", nativeSelectorNamesDerived()},
		{"Go witness count refusal", nativeOracleGoFile, `return nil, oracleFailure(KindLimitExceeded, "selected Go witnesses exceed 40 entries", "select at most 40 distinct literal witnesses")`, "return nil, nil", nativeSelectorNamesDerived()},
		{"package literal equality", nativeOracleGoFile, `valid = name == "go_package:./"+m.PackageCwd`, "valid = name == other", nativePackageWitnessDerived(true)},
		{"package duplicate refusal", nativeOracleGoFile, "if !valid || local[name] {", "if local[name] {", nativePackageWitnessDerived(true)},
		{"package cwd admission", "internal/store/native_recipe_case.go", "validateWorkContextRepoPath(m.PackageCwd)", "validateWorkContextRepoPath(other)", nativePackageWitnessDerived(false)},
		{"package observed input", nativeOracleGoFile, "result.ObservedWitnessIDs = slices.Clone(result.SelectedWitnessIDs)", "result.ObservedWitnessIDs = slices.Clone(other.SelectedWitnessIDs)", nativePackageObservedDerived()},
		{"package nested skip bound", "internal/store/native_go_events.go", "len(g.nestedSkips) >= 64", "len(other) >= 64", nativeInventorySkipCount(true)},
		{"package nested skip copy", "internal/store/native_go_package.go", "inventory.NestedSkippedTests = slices.Clone(g.nestedSkips)", "inventory.NestedSkippedTests = slices.Clone(other)", nativeInventorySkipCount(true)},
		{"package expected skip membership", "internal/store/native_go_events.go", "!slices.Contains(g.expected, test)", "!slices.Contains(other, test)", nativeInventorySkipCount(false)},
		{"package unique skip terminal", "internal/store/native_go_events.go", "!known || phase != 1 || nested && rootPhase != 1", "!known || nested && rootPhase != 1", nativeInventorySkipCount(false)},
		{"package input file guard", "internal/store/native_go_package.go", "len(files) > nativeGoInputFileLimit", "len(other) > nativeGoInputFileLimit", nativeRejectUpperBound("internal/store/native_go_package.go", "nativeGoPackageTestInventory", "len(files)", boundItems)},
		{"package input byte guard", "internal/store/native_go_package.go", "inventory.TestInputBytes > nativeGoInputByteLimit", "other.TestInputBytes > nativeGoInputByteLimit", nativeRejectUpperBound("internal/store/native_go_package.go", "nativeGoPackageTestInventory", "inventory.TestInputBytes", boundBytes)},
		{"package test count guard", "internal/store/native_go_package.go", "len(names) > 65536", "len(other) > 65536", nativeRejectUpperBound("internal/store/native_go_package.go", "nativeGoPackageTestInventory", "len(names)", boundItems)},
		{"target selected input", nativeOracleHostRun, "!slices.Equal(n.result.ObservedWitnessIDs, selected)", "!slices.Equal(n.result.ObservedWitnessIDs, other)", nativeObservedWitnessDerived()},
		{"target observed output", nativeOracleHostRun, "!slices.Equal(n.result.ObservedWitnessIDs, selected)", "!slices.Equal(other.ObservedWitnessIDs, selected)", nativeObservedWitnessDerived()},
		{"target mismatch refusal", nativeOracleHostRun, `return nativeUnavailable("observed witnesses differ from the exact selected population")`, "return nil", nativeObservedWitnessDerived()},
		{"target exact method owner", nativeOracleHostRun, "(n *nativeTargetOracleRun) finish", "(n *otherRun) finish", nativeObservedWitnessDerived()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := ledgerRepoRoot(t)
			raw, err := os.ReadFile(filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			original := string(raw)
			if strings.Count(original, tc.from) != 1 {
				t.Fatalf("mutation must target exactly one live source occurrence: %q", tc.from)
			}
			for _, mutated := range []bool{false, true} {
				rt := newLedgerRuntime(root)
				source := original
				if mutated {
					source = strings.Replace(source, tc.from, tc.to, 1)
				}
				if err := rt.parseGoSource(tc.file, source); err != nil {
					t.Fatal(err)
				}
				_, err := rt.evaluateSource(tc.source, nil)
				if !mutated && err != nil {
					t.Fatalf("live enforcing owner must prove: %v", err)
				}
				if mutated && err == nil {
					t.Fatal("the source proof accepted a broken enforcing owner")
				}
			}
		})
	}
}

func TestNativePackageInventoryNumericBounds(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	doc, err := rt.loadDoc(payloadSchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	properties := doc["$defs"].(map[string]any)["native_oracle_package_inventory"].(map[string]any)["properties"].(map[string]any)
	for _, tc := range []struct {
		field, measurement string
		unit               boundUnit
	}{
		{"test_file_count", "len(files)", boundItems},
		{"test_input_bytes", "inventory.TestInputBytes", boundBytes},
		{"test_count", "len(names)", boundItems},
		{"passed_count", "len(names)", boundItems},
	} {
		t.Run(tc.field, func(t *testing.T) {
			bound, err := rt.evaluateSource(nativeRejectUpperBound("internal/store/native_go_package.go", "nativeGoPackageTestInventory", tc.measurement, tc.unit), nil)
			if err != nil {
				t.Fatal(err)
			}
			maximum, err := strconv.ParseInt(fmt.Sprint(properties[tc.field].(map[string]any)["maximum"]), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			if len(bound.constraints) != 1 || bound.constraints[0].value != maximum {
				t.Fatalf("%s schema maximum %d differs from actual source guard %+v", tc.field, maximum, bound)
			}
		})
	}
}

// nativeAcquiredAtDerived proves the lease's acquired_at is written exactly
// through time.RFC3339Nano formatting and takes its width from the shared
// conservative layout constructor: pkTimeLayoutWidthBound resolves the
// stdlib constant and tokenizes the layout at machine widths without
// assuming a four-digit year. Any other layout in the writer fails closed.
func nativeAcquiredAtDerived() outputBoundSource {
	return pkTimeLayoutWidthBound(nativeWorktreesFile, "acquireVerifyLeaseTx", "time.RFC3339Nano")
}

// nativeProbeGatePos reports the source position of the first
// error-propagating probeWorktreeReachable gate carrying op in fn: the gate
// is if err := probeWorktreeReachable(...); err != nil { return ... }.
func nativeProbeGatePos(fn *ast.FuncDecl, op string) (token.Pos, error) {
	found := token.NoPos
	var problem error
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != token.NoPos || problem != nil {
			return false
		}
		branch, ok := n.(*ast.IfStmt)
		if !ok || branch.Init == nil || branch.Cond == nil || branch.Body == nil {
			return true
		}
		init, ok := branch.Init.(*ast.AssignStmt)
		if !ok || len(init.Lhs) != 1 || len(init.Rhs) != 1 {
			return true
		}
		errIdent, ok := init.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := init.Rhs[0].(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "probeWorktreeReachable") || len(call.Args) != 4 {
			return true
		}
		gateOp, ok := nativeStringLit(call.Args[2])
		if !ok || gateOp != op {
			return true
		}
		be, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || be.Op != token.NEQ {
			return true
		}
		left, leftOK := be.X.(*ast.Ident)
		right, rightOK := be.Y.(*ast.Ident)
		if !leftOK || !rightOK || left.Name != errIdent.Name || right.Name != "nil" {
			return true
		}
		returns := false
		ast.Inspect(branch.Body, func(m ast.Node) bool {
			if _, ok := m.(*ast.ReturnStmt); ok {
				returns = true
			}
			return true
		})
		if !returns {
			problem = fmt.Errorf("native bound: the %s probe gate no longer returns on error", op)
			return false
		}
		found = branch.Pos()
		return true
	})
	if problem != nil {
		return token.NoPos, problem
	}
	if found == token.NoPos {
		return token.NoPos, fmt.Errorf("native bound: no error-propagating probeWorktreeReachable(%q) gate remains", op)
	}
	return found, nil
}

// nativePathProjectionPos reports the position of the first composite entry
// that publishes a Path member straight from entry.Path in fn.
func nativePathProjectionPos(fn *ast.FuncDecl) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Path" {
			return true
		}
		if name, ok := nativeSelectorName(kv.Value); ok && name == "Path" {
			pos = kv.Pos()
		}
		return true
	})
	return pos
}

// nativeExecGitChdirProof verifies the runner's actual child wiring: the
// emitted full path travels as git's -C directory argument
// (command := append([]string{"-C", dir}, args...)) and the child failure
// returns to the caller. git performs the directory change itself, so a
// successful run is the kernel accepting the whole path.
func nativeExecGitChdirProof(execRun *ast.FuncDecl) error {
	gitC := false
	ast.Inspect(execRun, func(n ast.Node) bool {
		if gitC {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != "command" {
			return true
		}
		appendCall, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(appendCall, "append") || len(appendCall.Args) != 2 || !appendCall.Ellipsis.IsValid() {
			return true
		}
		lit, ok := appendCall.Args[0].(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 2 {
			return true
		}
		first, ok := nativeStringLit(lit.Elts[0])
		if !ok || first != "-C" {
			return true
		}
		dir, ok := lit.Elts[1].(*ast.Ident)
		if !ok || dir.Name != "dir" {
			return true
		}
		spread, ok := appendCall.Args[1].(*ast.Ident)
		if !ok || spread.Name != "args" {
			return true
		}
		gitC = true
		return true
	})
	if !gitC || len(nativeCalls(execRun, "exec.Command")) == 0 {
		return fmt.Errorf("native bound: ExecGitRunner.Run no longer runs git -C dir with the variadic argv spread")
	}
	returnsErr := false
	ast.Inspect(execRun, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			if ident, ok := result.(*ast.Ident); ok && ident.Name == "err" {
				returnsErr = true
			}
		}
		return true
	})
	if !returnsErr {
		return fmt.Errorf("native bound: ExecGitRunner.Run no longer returns the child error")
	}
	return nil
}

// nativeProbeFirstCommandProof verifies the probe's first command runs the
// emitted full path (entry.Path) with git rev-parse and refuses typed on
// error: that successful probe is the kernel's acceptance of the path.
func nativeProbeFirstCommandProof(probe *ast.FuncDecl) error {
	if probe.Body == nil || len(probe.Body.List) == 0 {
		return fmt.Errorf("native bound: probeWorktreeReachable has no probe statement")
	}
	first, ok := probe.Body.List[0].(*ast.AssignStmt)
	if !ok || len(first.Rhs) != 1 {
		return fmt.Errorf("native bound: probeWorktreeReachable no longer opens with the git probe")
	}
	runCall, ok := first.Rhs[0].(*ast.CallExpr)
	if !ok || !ledgerCalleeMatches(runCall, "Run") || len(runCall.Args) < 3 {
		return fmt.Errorf("native bound: probeWorktreeReachable no longer opens with the git probe")
	}
	if name, ok := nativeSelectorName(runCall.Args[1]); !ok || name != "Path" {
		return fmt.Errorf("native bound: the probe no longer runs in entry.Path")
	}
	revParse := false
	for _, arg := range runCall.Args[2:] {
		if lit, ok := nativeStringLit(arg); ok && lit == "rev-parse" {
			revParse = true
		}
	}
	if !revParse {
		return fmt.Errorf("native bound: the probe no longer runs git rev-parse")
	}
	return nil
}

// nativePathRouteProof verifies one projecting route: an error-propagating
// probeWorktreeReachable(op) gate that precedes the route's entry.Path
// projection, so no result is built after a refused probe.
func nativePathRouteProof(fn *ast.FuncDecl, op string) error {
	gate, err := nativeProbeGatePos(fn, op)
	if err != nil {
		return err
	}
	projection := nativePathProjectionPos(fn)
	if projection == token.NoPos || gate > projection {
		return fmt.Errorf("native bound: %s no longer projects entry.Path behind its %q probe gate", fn.Name.Name, op)
	}
	return nil
}

// nativeOracleRouteProof verifies the native oracle route: the probe gate
// precedes the nativeOracleInitialResult call, and that constructor projects
// entry.Path into the published result.
func nativeOracleRouteProof(oracle, oracleInit *ast.FuncDecl) error {
	gate, err := nativeProbeGatePos(oracle, "worktree_verify")
	if err != nil {
		return err
	}
	initialCallPos := token.NoPos
	for _, call := range nativeCalls(oracle, "nativeOracleInitialResult") {
		if initialCallPos == token.NoPos || call.Pos() < initialCallPos {
			initialCallPos = call.Pos()
		}
	}
	if initialCallPos == token.NoPos || gate > initialCallPos {
		return fmt.Errorf("native bound: the native oracle route no longer builds its result behind the probe gate")
	}
	if nativePathProjectionPos(oracleInit) == token.NoPos {
		return fmt.Errorf("native bound: nativeOracleInitialResult no longer projects entry.Path")
	}
	return nil
}

// nativePathMaxProbeDerived proves the projected worktree path was accepted
// whole by the kernel through the exact read that projects it. Both
// projecting routes gate their result behind an error-propagating
// probeWorktreeReachable whose first command is git -C entry.Path through
// ExecGitRunner: a path argument longer than syscall.PathMax-1 makes git's
// own directory change fail and the failure propagates as a typed refusal
// before any result is built, so a successfully projected path carries at
// most syscall.PathMax-1 bytes. The constant is read from the running
// pinned toolchain's src/syscall/ztypes_linux_amd64.go (0x1000) and the row
// is refused on any other release platform. The derivation never touches
// the unchecked join that produced the stored value; it is grounded only in
// the successful kernel operation the read performed on the emitted full
// path. The causal shapes are pure AST helpers, so synthetic mutation
// witnesses drive them directly.
func nativePathMaxProbeDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		width, err := nativePathSuccessfulSyscallWidth(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("derived successful-probe path width: every projecting read first runs git -C entry.Path through probeWorktreeReachable and returns its failure, so a projected path was accepted whole by the kernel and carries at most syscall.PathMax-1 = %d bytes (pinned toolchain ztypes_linux_amd64.go, linux/amd64)", width)
		return evaluatedBound{
			kind:        "derived",
			describe:    describe,
			constraints: []boundConstraint{{unit: boundBytes, value: width, from: describe}},
		}, nil
	}}
}

// nativePathSuccessfulSyscallWidth pins syscall.PathMax from the running
// pinned toolchain and verifies the live successful-path causal wiring on
// the real projecting routes.
func nativePathSuccessfulSyscallWidth(rt *ledgerRuntime) (int64, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return 0, fmt.Errorf("native bound: the syscall.PathMax derivation applies only on the linux/amd64 release platform")
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return 0, fmt.Errorf("native bound: cannot resolve GOROOT from the go toolchain on PATH: %w", err)
	}
	goRoot := strings.TrimSpace(string(out))
	if goRoot == "" {
		return 0, fmt.Errorf("native bound: the go toolchain on PATH reports no GOROOT")
	}
	ztypes := filepath.Join("src", "syscall", "ztypes_linux_amd64.go")
	raw, err := os.ReadFile(filepath.Join(goRoot, filepath.FromSlash(ztypes)))
	if err != nil {
		return 0, fmt.Errorf("native bound: the pinned toolchain syscall types: %w", err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), ztypes, raw, parser.SkipObjectResolution)
	if err != nil {
		return 0, fmt.Errorf("native bound: the pinned toolchain syscall types do not parse: %w", err)
	}
	pathMax, found := int64(0), false
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
				continue
			}
			if valueSpec.Names[0].Name != "PathMax" {
				continue
			}
			if value, ok := nativeIntLit(valueSpec.Values[0]); ok {
				pathMax, found = value, true
			}
		}
	}
	if !found || pathMax != 0x1000 {
		return 0, fmt.Errorf("native bound: the pinned toolchain no longer pins syscall.PathMax at 0x1000 in %s", ztypes)
	}
	execRun, err := nativeFuncDecl(rt, nativeProjectLocator, "Run")
	if err != nil {
		return 0, err
	}
	if err := nativeExecGitChdirProof(execRun); err != nil {
		return 0, err
	}
	probe, err := nativeFuncDecl(rt, nativeWorktreesFile, "probeWorktreeReachable")
	if err != nil {
		return 0, err
	}
	if err := nativeProbeFirstCommandProof(probe); err != nil {
		return 0, err
	}
	inspect, err := nativeFuncDecl(rt, nativeWorktreesFile, "InspectWorktree")
	if err != nil {
		return 0, err
	}
	if err := nativePathRouteProof(inspect, "worktree_inspect"); err != nil {
		return 0, err
	}
	verify, err := nativeFuncDecl(rt, nativeWorktreesFile, "VerifyWorktree")
	if err != nil {
		return 0, err
	}
	if err := nativePathRouteProof(verify, "worktree_verify"); err != nil {
		return 0, err
	}
	oracle, err := nativeFuncDecl(rt, nativeOracleFile, "verifyNativeOracle")
	if err != nil {
		return 0, err
	}
	oracleInit, err := nativeFuncDecl(rt, nativeOracleFile, "nativeOracleInitialResult")
	if err != nil {
		return 0, err
	}
	if err := nativeOracleRouteProof(oracle, oracleInit); err != nil {
		return 0, err
	}
	return pathMax - 1, nil
}

// nativeSelectorNameCount derives the full literal-witness cardinality from
// the explicit set guard. Root-selector length cannot bound nested witnesses.
func nativeSelectorNameCount(rt *ledgerRuntime) (int64, string, error) {
	caseTests, err := nativeFuncDecl(rt, nativeOracleGoFile, "nativeOracleCaseTests")
	if err != nil {
		return 0, "", err
	}
	if len(nativeCalls(caseTests, "nativeGoWitnessValid")) != 1 {
		return 0, "", fmt.Errorf("native bound: selected witnesses no longer pass the literal witness validator")
	}
	bound, err := rt.evaluateSource(nativeRejectUpperBound(nativeOracleGoFile, "nativeOracleCaseTests", "len(set)", boundItems), nil)
	if err != nil {
		return 0, "", err
	}
	// The guarded set must be the source of every returned witness, not a
	// separate count whose admission can drift away from the result.
	makeFound, rangeFound, appendFound, returnFound := false, false, false, false
	ast.Inspect(caseTests, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if len(node.Lhs) == 1 && len(node.Rhs) == 1 {
				lhs, rhs := ledgerExprText(node.Lhs[0]), ledgerExprText(node.Rhs[0])
				makeFound = makeFound || lhs == "names" && rhs == "make([]string, 0, len(set))"
				appendFound = appendFound || lhs == "names" && rhs == "append(names, name)"
			}
		case *ast.RangeStmt:
			rangeFound = rangeFound || ledgerExprText(node.X) == "set" && ledgerExprText(node.Key) == "name"
		case *ast.ReturnStmt:
			returnFound = returnFound || len(node.Results) == 2 && ledgerExprText(node.Results[0]) == "names" && ledgerExprText(node.Results[1]) == "nil"
		}
		return true
	})
	if !makeFound || !rangeFound || !appendFound || !returnFound {
		return 0, "", fmt.Errorf("native bound: nativeOracleCaseTests no longer returns the guarded distinct set")
	}
	for _, constraint := range bound.constraints {
		if constraint.unit == boundItems {
			return constraint.value, "explicit distinct literal-witness set guard: " + bound.describe, nil
		}
	}
	return 0, "", fmt.Errorf("native bound: selected witness guard carries no item capacity")
}

// nativeSelectorNamesDerived closes the Go family selected_test_names rows.
func nativeSelectorNamesDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		namesMax, describe, err := nativeSelectorNameCount(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		return nativeItemsWidth(namesMax, describe), nil
	}}
}

// nativeObservedNamesDerived closes the Go family observed_test_names rows:
// consumeStage clones result.SelectedTestNames, so the observed population
// inherits the explicit selected-witness cardinality guard.
func nativeObservedNamesDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		namesMax, describe, err := nativeSelectorNameCount(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		run, err := nativeMethodDecl(rt, nativeOracleGoFile, "nativeGoOracleRun", "consumeStage")
		if err != nil {
			return evaluatedBound{}, err
		}
		joined := false
		ast.Inspect(run, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			if len(assign.Lhs) != 1 || ledgerExprText(assign.Lhs[0]) != "result.ObservedTestNames" {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "slices.Clone") || len(call.Args) != 1 {
				return true
			}
			joined = ledgerExprText(call.Args[0]) == "result.SelectedTestNames"
			return true
		})
		if !joined {
			return evaluatedBound{}, fmt.Errorf("native bound: nativeGoOracleRun.consumeStage no longer derives ObservedTestNames from result.SelectedTestNames")
		}
		return nativeItemsWidth(namesMax, describe+"; observed population equals the selected set (slices.Clone)"), nil
	}}
}

// nativeWitnessSelectorCount derives the target family's distinct selected
// witness bound from the rendered verify-command shape: nativeTargetSelector
// prepends exactly one runner literal to the witness list and refuses the
// render over maxWorktreeVerifyCommandArgs parts.
func nativeWitnessSelectorCount(rt *ledgerRuntime) (int64, string, error) {
	selector, err := nativeFuncDecl(rt, nativeOracleHostFile, "nativeTargetSelector")
	if err != nil {
		return 0, "", err
	}
	appends := 0
	ast.Inspect(selector, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "append") || len(call.Args) != 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 1 {
			return true
		}
		if _, ok := nativeStringLit(lit.Elts[0]); !ok {
			return true
		}
		ident, ok := call.Args[1].(*ast.Ident)
		if !ok || ident.Name != "witnesses" {
			return true
		}
		appends++
		return true
	})
	if appends == 0 {
		return 0, "", fmt.Errorf("native bound: nativeTargetSelector no longer prepends one runner literal to the witness list")
	}
	if len(nativeCalls(selector, "validateWorktreeVerifyCommand")) == 0 {
		return 0, "", fmt.Errorf("native bound: nativeTargetSelector no longer validates the rendered selector command shape")
	}
	guard, err := nativeFuncDecl(rt, nativeWorktreesFile, "validateWorktreeVerifyCommand")
	if err != nil {
		return 0, "", err
	}
	budget, found := int64(0), false
	ast.Inspect(guard, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.GTR {
			return true
		}
		lengthCall, ok := be.X.(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(lengthCall, "len") || len(lengthCall.Args) != 1 {
			return true
		}
		ident, ok := lengthCall.Args[0].(*ast.Ident)
		if !ok || ident.Name != "command" {
			return true
		}
		yIdent, ok := be.Y.(*ast.Ident)
		if !ok {
			return true
		}
		goS, err := nativeGoFile(rt, nativeWorktreesFile)
		if err != nil {
			return true
		}
		if value, err := rt.evalNumeric(goS, guard, yIdent); err == nil {
			budget, found = value, true
		}
		return true
	})
	if !found || budget < 2 {
		return 0, "", fmt.Errorf("native bound: the verify-command part-count guard is no longer derivable")
	}
	witnessesMax := budget - 1
	describe := fmt.Sprintf("derived verify-command shape cardinality: the %d-part command budget with one runner literal admits at most %d selected witnesses", budget, witnessesMax)
	return witnessesMax, describe, nil
}

// nativeWitnessSelectorDerived closes the target family selected_witness_ids
// rows.
func nativeWitnessSelectorDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		witnessesMax, describe, err := nativeWitnessSelectorCount(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		return nativeItemsWidth(witnessesMax, describe), nil
	}}
}

// nativeObservedWitnessDerived closes the target family observed_witness_ids
// rows: the execute run refuses unless the observed witnesses equal the
// selected population, so the observed cardinality inherits the selector
// bound.
func nativeObservedWitnessDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		witnessesMax, describe, err := nativeWitnessSelectorCount(rt)
		if err != nil {
			return evaluatedBound{}, err
		}
		run, err := nativeMethodDecl(rt, nativeOracleHostRun, "nativeTargetOracleRun", "finish")
		if err != nil {
			return evaluatedBound{}, err
		}
		equalityFound := false
		ast.Inspect(run, func(node ast.Node) bool {
			branch, ok := node.(*ast.IfStmt)
			if ok && ledgerExprText(branch.Cond) == "!slices.Equal(n.result.ObservedWitnessIDs, selected)" && nativeUnavailableReturn(branch.Body) {
				equalityFound = true
			}
			return true
		})
		if !equalityFound {
			return evaluatedBound{}, fmt.Errorf("native bound: nativeTargetOracleRun.finish no longer refuses observed witnesses that differ from the selected population")
		}
		return nativeItemsWidth(witnessesMax, describe+"; the execute join refuses observed witnesses that differ from the selected set"), nil
	}}
}

// nativeFixedShapeDerived proves the verify mutation result publishes fixed
// literal shapes: exactly one changed ref, exactly one next-valid intent, and
// exactly two required field names, read from the live literals in
// mutateWorktreeVerify and the 1:1 projection helpers. What: "changed_refs",
// "next_valid_intents", or "required_fields".
func nativeFixedShapeDerived(what string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		mint, err := nativeFuncDecl(rt, nativeMutationsFile, "mutateWorktreeVerify")
		if err != nil {
			return evaluatedBound{}, err
		}
		count, found := int64(0), false
		var projections []string
		switch what {
		case "changed_refs":
			calls := nativeCalls(mint, "mutationResultChangedRefs")
			if len(calls) != 1 || len(calls[0].Args) != 1 {
				return evaluatedBound{}, fmt.Errorf("native bound: mutateWorktreeVerify no longer publishes changed refs through exactly one fixed literal")
			}
			lit, ok := calls[0].Args[0].(*ast.CompositeLit)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("native bound: the changed_refs argument is no longer a slice literal")
			}
			count, found = int64(len(lit.Elts)), true
			projections = []string{"mutationResultChangedRefs"}
		case "next_valid_intents", "required_fields":
			calls := nativeCalls(mint, "mutationResultIntents")
			if len(calls) != 1 || len(calls[0].Args) != 1 {
				return evaluatedBound{}, fmt.Errorf("native bound: mutateWorktreeVerify no longer publishes intents through exactly one fixed literal")
			}
			ident, ok := calls[0].Args[0].(*ast.Ident)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("native bound: the intents argument is no longer the fixed intents variable")
			}
			var intentsLit *ast.CompositeLit
			ast.Inspect(mint, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				lhs, ok := assign.Lhs[0].(*ast.Ident)
				if !ok || lhs.Name != ident.Name {
					return true
				}
				if lit, ok := assign.Rhs[0].(*ast.CompositeLit); ok {
					intentsLit = lit
				}
				return true
			})
			if intentsLit == nil || len(intentsLit.Elts) != 1 {
				return evaluatedBound{}, fmt.Errorf("native bound: the intents literal is no longer a one-element slice literal")
			}
			if what == "next_valid_intents" {
				count, found = int64(len(intentsLit.Elts)), true
			} else {
				element, ok := intentsLit.Elts[0].(*ast.CompositeLit)
				if !ok {
					return evaluatedBound{}, fmt.Errorf("native bound: the intents element is no longer a struct literal")
				}
				for _, elt := range element.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "RequiredFields" {
						continue
					}
					fields, ok := kv.Value.(*ast.CompositeLit)
					if !ok {
						return evaluatedBound{}, fmt.Errorf("native bound: RequiredFields is no longer a slice literal")
					}
					count, found = int64(len(fields.Elts)), true
				}
			}
			projections = []string{"mutationResultIntents"}
		default:
			return evaluatedBound{}, fmt.Errorf("native bound: unknown fixed shape %q", what)
		}
		if !found {
			return evaluatedBound{}, fmt.Errorf("native bound: the %s literal shape is no longer derivable", what)
		}
		for _, projection := range projections {
			fn, err := nativeFuncDecl(rt, nativeMutationsFile, projection)
			if err != nil {
				return evaluatedBound{}, err
			}
			appends, ranges := 0, 0
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.RangeStmt:
					ranges++
				case *ast.CallExpr:
					if ledgerCalleeMatches(node, "append") {
						appends++
					}
				}
				return true
			})
			if ranges != 1 || appends != 1 {
				return evaluatedBound{}, fmt.Errorf("native bound: %s is no longer a 1:1 projection", projection)
			}
		}
		return nativeItemsWidth(count, fmt.Sprintf("derived fixed %s shape: the verify result publishes exactly %d elements (live literal in mutateWorktreeVerify)", what, count)), nil
	}}
}

// nativeInspectContentDerived proves every InspectWorktree mode publishes
// content through boundText(..., maxBytes) and that the sole production
// caller omits MaxBytes, so the effective cap is defaultWorktreeInspectBytes.
func nativeInspectContentDerived() outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		inspect, err := nativeFuncDecl(rt, nativeWorktreesFile, "InspectWorktree")
		if err != nil {
			return evaluatedBound{}, err
		}
		fallbackFound, bounded, unbounded := false, 0, 0
		ast.Inspect(inspect, func(n ast.Node) bool {
			if branch, ok := n.(*ast.IfStmt); ok && branch.Cond != nil {
				if be, ok := branch.Cond.(*ast.BinaryExpr); ok && be.Op == token.LEQ {
					if ident, ok := be.X.(*ast.Ident); ok && ident.Name == "maxBytes" {
						if value, ok := nativeIntLit(be.Y); ok && value == 0 {
							ast.Inspect(branch.Body, func(m ast.Node) bool {
								assign, ok := m.(*ast.AssignStmt)
								if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
									return true
								}
								lhs, ok := assign.Lhs[0].(*ast.Ident)
								rhs, ok2 := assign.Rhs[0].(*ast.Ident)
								if ok && ok2 && lhs.Name == "maxBytes" && rhs.Name == "defaultWorktreeInspectBytes" {
									fallbackFound = true
								}
								return true
							})
						}
					}
				}
			}
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if name, ok := nativeSelectorName(lhs); !ok || name != "Content" {
					continue
				}
				// result.Content, result.Truncated = boundText(text, maxBytes)
				call, ok := assign.Rhs[0].(*ast.CallExpr)
				if len(assign.Rhs) == 1 && ok && ledgerCalleeMatches(call, "boundText") && len(call.Args) == 2 {
					if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "maxBytes" {
						bounded++
						continue
					}
				}
				unbounded++
			}
			return true
		})
		if !fallbackFound || bounded == 0 || unbounded != 0 {
			return evaluatedBound{}, fmt.Errorf("native bound: InspectWorktree must fall back to defaultWorktreeInspectBytes and assign result.Content only from boundText(..., maxBytes); found %d bounded and %d other assignments", bounded, unbounded)
		}
		reader, err := nativeFuncDecl(rt, nativeWorktreesFile, "boundText")
		if err != nil {
			return evaluatedBound{}, err
		}
		sliced := false
		ast.Inspect(reader, func(n ast.Node) bool {
			slice, ok := n.(*ast.SliceExpr)
			if !ok || slice.Low != nil || slice.Slice3 {
				return true
			}
			if ident, ok := slice.High.(*ast.Ident); ok && ident.Name == "maxBytes" {
				sliced = true
			}
			return true
		})
		if !sliced {
			return evaluatedBound{}, fmt.Errorf("native bound: boundText no longer slices text to [:maxBytes]")
		}
		callerSites := 0
		for _, dir := range []string{"internal/agent", "internal/store"} {
			entries, err := os.ReadDir(filepath.Join(rt.root, filepath.Clean(dir)))
			if err != nil {
				return evaluatedBound{}, err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				name := filepath.ToSlash(filepath.Join(dir, entry.Name()))
				goS, err := rt.loadGo(name)
				if err != nil {
					continue
				}
				for _, decl := range goS.file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok {
						continue
					}
					for _, call := range nativeCalls(fn, "InspectWorktree") {
						callerSites++
						var request ast.Expr
						for _, arg := range call.Args {
							if lit, ok := arg.(*ast.CompositeLit); ok {
								request = lit
							}
						}
						if request == nil {
							return evaluatedBound{}, fmt.Errorf("native bound: the InspectWorktree call in %s:%s carries no request literal", name, fn.Name.Name)
						}
						lit := request.(*ast.CompositeLit)
						for _, elt := range lit.Elts {
							kv, ok := elt.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "MaxBytes" {
								return evaluatedBound{}, fmt.Errorf("native bound: the InspectWorktree caller in %s now sets MaxBytes; the default-cap derivation no longer holds", name)
							}
						}
					}
				}
			}
		}
		if callerSites != 1 {
			return evaluatedBound{}, fmt.Errorf("native bound: %d production InspectWorktree call sites found; the sole-caller derivation requires exactly one", callerSites)
		}
		capValue, err := nativeConstValue(rt, nativeWorktreesFile, "defaultWorktreeInspectBytes")
		if err != nil {
			return evaluatedBound{}, err
		}
		width := capValue
		describe := fmt.Sprintf("derived inspect content: boundText(..., maxBytes) under the defaultWorktreeInspectBytes fallback publishes %d bytes", width)
		return evaluatedBound{
			kind:        "derived",
			describe:    describe,
			constraints: []boundConstraint{{unit: boundBytes, value: width, from: describe}},
		}, nil
	}}
}

// nativePathWitnessRunner mirrors the live ExecGitRunner.Run child wiring.
const nativePathWitnessRunner = `package store

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("empty git directory")
	}
	command := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", command...)
	stdout, _, err := runBoundedGitOutput(ctx, cmd)
	return stdout, err
}
`

// nativePathWitnessProbe mirrors the live probe's first command.
const nativePathWitnessProbe = `package store

func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return wrapFailure(err)
	}
	return nil
}
`

// nativePathWitnessRoute mirrors one projecting route: the probe gate precedes
// the entry.Path projection.
const nativePathWitnessRoute = `package store

func inspectWitness(ctx context.Context, runner GitRunner, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID, Path: entry.Path}
	return result, nil
}
`

// nativePathWitnessOracle mirrors the oracle route: the probe gate precedes
// the result constructor, which projects entry.Path.
const nativePathWitnessOracle = `package store

func verifyWitness(ctx context.Context, runner GitRunner, entry WorktreeEntry, plan nativeOraclePlan) (WorktreeVerifyResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	result := nativeOracleInitialResult(entry, plan)
	return result, nil
}

func nativeOracleInitialResult(entry WorktreeEntry, plan nativeOraclePlan) WorktreeVerifyResult {
	r := WorktreeVerifyResult{Path: entry.Path}
	return r
}
`

// TestNativePathSuccessfulPathProofMutations drives the successful-path
// causal helpers over synthetic witnesses: the live shapes must prove, and
// dropping the -C wiring, moving the directory off the child argv, swallowing
// the child error, probing a fixed directory, running another subcommand,
// swallowing the gate error, projecting before the gate, or dropping the
// projection must each refuse.
func TestNativePathSuccessfulPathProofMutations(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	witness := func(file, function, source string) *ast.FuncDecl {
		t.Helper()
		if err := rt.parseGoSource(file, source); err != nil {
			t.Fatalf("%s does not parse: %v", file, err)
		}
		fn, err := nativeFuncDecl(rt, file, function)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		return fn
	}
	greenRunner := witness("path_witness_runner.go", "Run", nativePathWitnessRunner)
	if err := nativeExecGitChdirProof(greenRunner); err != nil {
		t.Fatalf("the live runner shape must prove: %v", err)
	}
	greenProbe := witness("path_witness_probe.go", "probeWorktreeReachable", nativePathWitnessProbe)
	if err := nativeProbeFirstCommandProof(greenProbe); err != nil {
		t.Fatalf("the live probe shape must prove: %v", err)
	}
	greenRoute := witness("path_witness_route.go", "inspectWitness", nativePathWitnessRoute)
	if err := nativePathRouteProof(greenRoute, "worktree_inspect"); err != nil {
		t.Fatalf("the live route shape must prove: %v", err)
	}
	oracleSource := witness("path_witness_oracle.go", "verifyWitness", nativePathWitnessOracle)
	oracleInit := witness("path_witness_oracle.go", "nativeOracleInitialResult", nativePathWitnessOracle)
	if err := nativeOracleRouteProof(oracleSource, oracleInit); err != nil {
		t.Fatalf("the live oracle shape must prove: %v", err)
	}
	cases := []struct {
		name     string
		file     string
		function string
		source   string
		check    func(rt *ledgerRuntime, fn *ast.FuncDecl) error
	}{
		{
			name: "runner without the -C flag", file: "path_mut_runner_noflag.go", function: "Run",
			source: `package store

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := append([]string{dir}, args...)
	cmd := exec.Command("git", command...)
	stdout, _, err := runBoundedGitOutput(ctx, cmd)
	return stdout, err
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativeExecGitChdirProof(fn) },
		},
		{
			name: "runner with the directory off the child argv", file: "path_mut_runner_cmddir.go", function: "Run",
			source: `package store

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := append([]string{}, args...)
	cmd := exec.Command("git", command...)
	cmd.Dir = dir
	stdout, _, err := runBoundedGitOutput(ctx, cmd)
	return stdout, err
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativeExecGitChdirProof(fn) },
		},
		{
			name: "runner swallowing the child error", file: "path_mut_runner_swallow.go", function: "Run",
			source: `package store

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", command...)
	stdout, _, err := runBoundedGitOutput(ctx, cmd)
	return stdout, nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativeExecGitChdirProof(fn) },
		},
		{
			name: "probe running a fixed directory", file: "path_mut_probe_fixeddir.go", function: "probeWorktreeReachable",
			source: `package store

func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, ".", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return wrapFailure(err)
	}
	return nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativeProbeFirstCommandProof(fn) },
		},
		{
			name: "probe running another subcommand", file: "path_mut_probe_status.go", function: "probeWorktreeReachable",
			source: `package store

func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "status", "--porcelain")
	if err != nil {
		return wrapFailure(err)
	}
	return nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativeProbeFirstCommandProof(fn) },
		},
		{
			name: "route swallowing the gate error", file: "path_mut_route_swallow.go", function: "inspectWitness",
			source: `package store

func inspectWitness(ctx context.Context, runner GitRunner, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		_ = err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID, Path: entry.Path}
	return result, nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") },
		},
		{
			name: "route projecting before the gate", file: "path_mut_route_order.go", function: "inspectWitness",
			source: `package store

func inspectWitness(ctx context.Context, runner GitRunner, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	result := WorktreeInspectResult{WorkID: req.WorkID, Path: entry.Path}
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	return result, nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") },
		},
		{
			name: "route dropping the path projection", file: "path_mut_route_nopath.go", function: "inspectWitness",
			source: `package store

func inspectWitness(ctx context.Context, runner GitRunner, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID}
	return result, nil
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error { return nativePathRouteProof(fn, "worktree_inspect") },
		},
		{
			name: "oracle dropping the path projection", file: "path_mut_oracle_nopath.go", function: "verifyWitness",
			source: `package store

func verifyWitness(ctx context.Context, runner GitRunner, entry WorktreeEntry, plan nativeOraclePlan) (WorktreeVerifyResult, error) {
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	result := nativeOracleInitialResult(entry, plan)
	return result, nil
}

func nativeOracleInitialResult(entry WorktreeEntry, plan nativeOraclePlan) WorktreeVerifyResult {
	r := WorktreeVerifyResult{WorkID: entry.WorkID}
	return r
}
`,
			check: func(rt *ledgerRuntime, fn *ast.FuncDecl) error {
				init, err := nativeFuncDecl(rt, "path_mut_oracle_nopath.go", "nativeOracleInitialResult")
				if err != nil {
					return err
				}
				return nativeOracleRouteProof(fn, init)
			},
		},
	}
	for _, tc := range cases {
		fn := witness(tc.file, tc.function, tc.source)
		if err := tc.check(rt, fn); err == nil {
			t.Fatalf("%s: the mutated shape must refuse", tc.name)
		}
	}
}
