# Local verification

Use this guide to choose repository checks. The shared sufficiency and evidence
reuse rule remains in [completion.md](.concord/instructions/completion.md).
[Required CI](.github/workflows/ci.yml) owns broad merge verification;
[Lefthook](lefthook.yml) owns pre-push checks. Neither is replaced by a local plan.

## Choose before running

Record the following in the existing assignment, PR or test report, not a second
tracking file:

1. Name the changed behavior and its owning functions or entry points.
2. Follow callers, including tests of callers in other packages. Inspect shared
   fixtures, embedded files, generated inputs and runtime configuration. Test-name
   similarity and a random shard do not establish coverage.
3. List the existing assertions that exercise those paths, plus the new regression.
   A regression must fail without the fix. Include affected caller tests, not just
   new tests of the edited function.
4. Record exact commands, required tools/environment, exclusions and why the
   selection is sufficient. State any unknown mapping and the wider check it needs.
5. Reuse applicable results. Rerun after relevant source, inputs or environment
   change, or when required by the owning gate. Do not repeat all CI by default.

Do not infer execution from exit zero: Go accepts zero-match `-run` expressions,
and a skipped test is not an executed assertion. Confirm observed names and outcomes
in native results or full verbose output. Compile-only checks are a distinct claim.

## Admission and plans

`bin/oc-test targeted` accepts explicit Go packages with finite anchored top-level
names, explicit adapter test files, or one repository Python check/test script.
This command-shape guard prevents accidental broad runs; it is not a cost bound,
changed-file analysis, or proof that the selection exists. A named integration test
can still need heavy admission. Use `broad` whenever the workload is expensive.

```sh
bin/oc-test plan targeted -- go test -run '^TestMainCheckoutLifecyclePreservesTerminalGates$' ./internal/agent
bin/oc-test targeted -- go test -run '^TestMainCheckoutLifecyclePreservesTerminalGates$' ./internal/agent -v
bin/oc-test targeted -- python3 scripts/test-oc-test.py
```

`plan` only parses arguments. It never runs `go test -list`, TestMain, test
initialization, dependency discovery or tests. It shows expected names, command,
admission and its limited evidence claim. Unknown command grammars, shell scripts,
whole packages, wildcard packages, benchmarks, fuzzing, regex subtest selection
and suite/shard execution use the explicit `broad` tier:

```sh
bin/oc-test plan broad -- go test ./internal/store
bin/oc-test broad -- go test -timeout=15m ./internal/store
bin/oc-test broad -- env -u CONCORD_SELECTED_PRODUCT_ID bun test adapter/opencode/
```

`broad` and conformance use the existing host gate's `full` admission; smoke and
preflight use its `smoke` admission. Heavy tiers fail closed if `oc-test-gate` is
missing. There is no repository scheduler or alternate lock. The host reports
queue wait separately from execution when available. Full admission defaults to
a 20-minute execution limit after admission; the existing host variable
`OC_TEST_GATE_FULL_TIMEOUT` can set an explicitly justified run budget. Do not
increase timeouts as a performance fix or disable required freshness checks.

Keep discovery inside admission. Shell command substitution happens **before**
the outer wrapper starts; wrap discovery and execution together, for store shard 1 of 7:

```sh
bin/oc-test broad -- bash -euo pipefail -c 'go test -timeout=15m -run "$(python3 scripts/go-test-shard.py ./internal/store 1 7)" ./internal/store'
```

Sharding assigns the discovered inventory, not affected behavior. A shard is a
CI-placement tool, not a default local coverage choice. `go test -list` executes
initialization/TestMain and is not a non-executing planning operation.

## Selection recipes and widening

| Change or risk | Focused iteration | Widen when |
|---|---|---|
| One Go behavior | Anchored named regression plus existing owner and direct-caller tests in explicit packages | A caller, runtime input or test dependency cannot be bounded |
| Store fixture, transaction or connection handling | Owner tests plus transaction/rollback/connection-invariant consumers; private database per test | Shared setup changes affect the package; admit the package or use required CI |
| Migration or current-state recovery | Relevant pre/post-migration and recovery tests, including failure/rollback paths | Migration ordering or shared registry changes affect all store opens |
| Native Go/Bun/uv integration | Relevant native-control cases with CI's pinned tools and prepared offline caches | Preparation or toolchain changes affect every native consumer |
| Adapter | Explicit changed test files and caller files, with Product selection cleared | Shared adapter contract, setup or native host behavior changes |
| Python tooling | One affected `scripts/test-*.py`; inspect imported helpers and callers | Unknown/transitive mapping or aggregate script changes need the tooling battery |
| Documentation | Link and public-content validators | Law/knowledge inputs change: run their owning validators too |
| Generated contracts | Edit manifest/schema, regenerate, then check generated parity and affected consumers | Shared schema/generator affects more than the edited example |

For example, a helper edited in `internal/store` can change an assertion reached
through `internal/agent`; selecting only a new store test misses that caller.
Follow the actual references before recording the command. Go import/test-import
and embed metadata can aid conservative package selection, but cannot prove
runtime dependencies. Do not claim exhaustive impact analysis from those fields.

New tests join the current inventory; renamed or deleted tests require reviewing
the intended assertion and correcting the explicit selection. Shared fixtures,
embedded inputs, generated/configuration changes, test-only imports and incomplete
dependency graphs require visible widening, not silent exclusion. Mixed-language
changes select each affected language. No automatic cross-language graph exists.

The existing Python advisory selector has a non-executing mode:

```sh
bin/oc-test broad -- python3 scripts/run-script-tests.py --dry-run -- scripts/local-test-plan.py
```

It derives suites from CI and follows local sibling Python imports and literal
script references transitively, with cycle detection. Dynamic imports, nonlocal
packages and runtime dependencies remain unknown. Inspect its selection and use a
documented wider run for missed callers. Its battery fallback is broad work.

Documentation checks:

```sh
bin/oc-test targeted -- python3 scripts/check-doc-links.py
bin/oc-test targeted -- python3 scripts/check-public-content.py
```

Generated-contract validation uses the owning generator/checker, including the
host typecheck; `--skip-host-typecheck` is incomplete evidence:

```sh
bin/oc-test broad -- python3 scripts/check-agent-contracts.py
```

## Suite ownership and placement

CI remains the executable inventory. Do not maintain a second test catalog.

* Store behavior, transactions, migrations and recovery stay in `internal/store`.
  Required store shard jobs cover their current discovered names; migrations and
  shared fixtures are not a reason to share writable databases or blanket-parallelize.
* Native-tool integration remains with its owning store/agent mechanisms. Use
  CI's Go/Bun/uv versions and offline preparation. Do not remove preparation from a
  shard without evidence that none of its tests needs it.
* `internal/agent` and `cmd/concord` have separate required shards. Unsharded
  Go packages use the `verify-tests-rest` job. Adapter jobs own Bun test files
  and existing timing artifacts; `verify-tooling` owns the Python battery.
* `bin/oc-test conformance` explicitly enables the long ten-process harness.
  Smoke excludes every sharded Go package, but includes the adapter suite and
  JSON/law validation. It is not a cheap substitute for a named regression.

Current deterministic round-robin sharding includes newly discovered tests and
refuses an empty shard. Preserve exhaustive/disjoint inventory checks when
changing placement. Duration-aware balancing and hotspot changes need comparable
measurements first. Do not sum overlapping parent/subtest durations or shard
elapsed times into an unsharded baseline. Do not add test execution just to time
an already-required run.

## Output and applicability

The wrapper streams stdout/stderr and preserves the child's exit status. Its
`PASS` means process success only. For a retained local log, choose an external
path and use a strict shell; never use `tail` as test evidence:

```sh
set -o pipefail
bin/oc-test targeted -- python3 scripts/test-oc-test.py 2>&1 | tee "$TEST_LOG"
```

Keep the complete log path and exit status in the existing report. A failing
child must stay failing even if logging/parsing succeeds. Missing reports,
interruption, parser errors and skipped expected tests remain visible gaps.
Arbitrary `broad` commands can provide only process-level evidence unless their
native tool supplies more. Local results are not Concord workflow verdicts.

Record tested commit and dirty state before/after, exact command, selected and
observed checks, interpreter/toolchain and relevant non-secret environment/cache
observations. If source changes during the run, applicability is uncertain; do
not silently bind it to the earlier commit. Prefer native caches over a second
result cache. Go `-count=1` deliberately forces fresh tests; race instrumentation
does not categorically disable caching, and timestamps alone cannot prove freshness.

For Python, keep suite status and the complete log; record `python3 --version`.

### Read the shared Go results

Required CI runs retain Go results through
[`scripts/go-test-results.py`](scripts/go-test-results.py). Consume that producer's
`events.jsonl`, `stderr.log`, `results.json`, `metadata.json` and bounded summary;
do not create another collector. Bun's existing CI timing artifacts remain
per-file measurements, not full test results.

For an already justified local Go run, the same producer can retain native output
inside broad admission. Use a new external artifact directory; it refuses reuse:

```sh
bin/oc-test broad -- python3 scripts/go-test-results.py --output-dir "$GO_RESULTS" -- go test -run '^TestMainCheckoutLifecyclePreservesTerminalGates$' ./internal/agent
bin/oc-test read-go "$GO_RESULTS" github.com/sharper-flow/concord/internal/agent=TestMainCheckoutLifecyclePreservesTerminalGates
```

`read-go` calls the same native reader without executing tests. It compares exact
package/name expectations, emits the producer's bounded schema with provenance
and full diagnostic locators, and fails for missing/skipped expectations or
incomplete/parser-failed evidence. A recorded nonzero process exit stays nonzero.
Without explicit expectations it reports observed results, not named coverage.
Compile-only remains labelled separately. Source applicability and cache labels
come from the original producer, not the reading machine's current checkout.

### Read native Bun results

The adapter CI execution also writes Bun's native JUnit report with
`--reporter=junit --reporter-outfile=...`. CI retains this as
`adapter-test-results-rest`, separately from
the unchanged per-file timing artifacts. The CI run, job, checkout and pinned Bun
setup supply provenance; console stdout/stderr remain in the same job's logs.
There is no second collector, result cache or extra adapter execution.

For an already justified local file selection, use CI's pinned Bun version and
choose a new report path outside the repository. Retain complete console output
as well; a stale report cannot establish the current execution:

```sh
bin/oc-test targeted -- bun test ./adapter/opencode/session-scope.test.ts --reporter=junit --reporter-outfile="$BUN_RESULTS"
```

Check the expected file and case names, child `failure`/`error`/`skipped` elements,
process status and complete report together. Bun 1.4.0 can return zero for a
skipped-only selection, an empty file or shard, or `--pass-with-no-tests`. Unmatched
names return nonzero and appear skipped in XML; a skipped case is not an executed
assertion. Empty selections, syntax errors and interrupted runs can leave no
completed XML. Missing reports remain evidence gaps; required CI uploads fail
if the report is absent. A timing file cannot replace the native results.

[`scripts/test-release-evidence.py`](scripts/test-release-evidence.py) checks the
real CI commands against newly discovered synthetic files and verifies these
native result boundaries with the exact pinned Bun runtime.
