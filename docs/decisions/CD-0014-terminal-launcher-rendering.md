# CD-0014: terminal launcher rendering stack

**Status:** Accepted
**Approval date:** 2026-08-10
**Approval:** Operator-approved architecture spike under GitHub issue #39
**Supersedes:** The rendering-dependency and query-scope sub-questions in C18
**Implementation status:** S1 shipped through issue #45 and PR #48. S2 Product
coordination, S3 Work detail, scoped search/knowledge, and identity-only OpenCode
handoff shipped through issue #51 and PR #52. Replacement readiness remains
unclaimed.

## Decision

Concord chooses Bubble Tea v2 behind an isolated renderer adapter:

- `charm.land/bubbletea/v2 v2.0.8`
- `charm.land/bubbles/v2 v2.1.1`
- `charm.land/lipgloss/v2 v2.0.5`

The framework-independent launcher model and read port live under
`internal/launcher`. All Bubble Tea, Bubbles, and Lip Gloss imports live under
`internal/launcher/render/bubbletea`. The renderer consumes snapshots and pure
screen projections; it does not define domain types, import the store, or call a
read from `View`/render methods.

The initial launcher semantic query is **Product-only**: it is available only
after an ambient Product exists on S2/S3 and never searches across Products. S1
has no semantic-query binding; `/` is a read-free local filter over the fetched
Product rows. The launcher remains read-only and does not answer approvals,
mutate durable state, derive workflow position, or create a second state authority.

## Evidence gate

The Bubble Tea spike passed all hard proofs on Linux amd64:

1. Repeated renders of unchanged state are byte-identical at fixed width and
   no-color profile.
2. No-color output retains textual reliance, degraded, stale, and error markers,
   stage, action count, focus, coverage, watermark, and age meaning. A byte-level
   control scanner rejects ESC, CSI, SGR, OSC, and other terminal control bytes;
   mutation tests inject representative ANSI/OSC sequences and verify rejection.
3. The representative S1 projection fits 80 display columns without horizontal-scroll
   dependence. The actual renderer uses `lipgloss.Width` while wrapping long ASCII,
   wide Unicode, and combining-mark values; tests assert every line is ≤80 and retain
   all five C14 row groups: identity, stage, reliance, action count, and focus.
4. Bubbles `textinput.Model` tests cover cursor movement, mid-string insertion,
   backspace and delete, actual `tea.PasteMsg` bracketed-paste handling, and
   clear through the framework key path.
5. `tea.WindowSizeMsg` is the one explicit resize event; it changes layout only.
6. A counting read port observes reads only on screen entry, query submit, and
   explicit refresh. No-op redraw and resize `Update` calls return no command and
   issue zero reads. Bubbles cursor-blink/input commands are executed in tests as
   UI-only commands and issue zero reads. The adapter contains no timer, poll,
   watcher, interval, `tea.Tick`, or `tea.Every` path. “No state polling” means no
   autonomous refresh loop; a foreground read remains allowed on those three
   explicit operator events.
7. AST plus `go list -deps` boundary checks cover every core Go file/package,
   reject Charm paths through the core closure, and reject store/domain/query paths
   from the renderer closure.
8. Focused tests, race tests, Linux amd64 tests, and the full repository gate
   command sequence (validators, gofmt, `go mod tidy`, vet, and race tests) pass.
   The dependency evidence test also passes in a copied checkout with no `.git`
   directory and a failing `git` executable, so dependency proof does not depend
   on staged, dirty, or committed state. `git diff --check`, public-content,
   documentation-link, and JSON checks pass.
9. `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./internal/launcher/...`
   reports `No vulnerabilities found.`
10. The inventory test derives production modules from `go list -deps -json` on the
    adapter, test-only modules from `go list -deps -test -json` minus that
    production closure, and module-graph-only modules by traversing an offline
    `go mod graph` loaded from a temporary module containing the exact direct Charm
    roots. It selects the highest graph version per path, then requires exact module
    and `/go.mod` entries in `go.sum`; it does not depend on selected-module cache
    metadata. Runtime/test module-cache evidence verifies
    actual cached license bytes; graph-only license evidence verifies checked-in
    bytes, so a clean module cache needs no graph-only module content. The test
    compares all three exact sets and all evidence links with the strict
    machine-readable inventory artifact below.

Commands used:

```text
go test ./internal/launcher/...
go test -race ./internal/launcher/...
GOOS=linux GOARCH=amd64 go test ./internal/launcher/...
GOOS=linux GOARCH=amd64 go build ./internal/launcher/render/bubbletea
bin/oc-test full
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./internal/launcher/...
go mod tidy
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go list -deps -json ./internal/launcher/render/bubbletea
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go list -deps -test -json ./internal/launcher/render/bubbletea
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go mod graph -modfile=<temporary direct-Charm go.mod>
```

## Dependency and license inventory

The single reviewed source is the strict machine-readable
[`CD-0014 terminal-launcher dependency inventory`](./CD-0014-terminal-launcher-dependencies.v1.json).
It records 19 runtime modules, zero test-only modules, and 4 module-graph-only
modules. The runtime and test-only groups come from the two offline adapter
closures. The module-graph-only group contains selected module versions reachable
by traversing `go mod graph` from the exact direct Charm roots but absent from both
closures. They are graph evidence only: they are not linked into the launcher
binary or test closure and no module-cache directory is required for them. Graph
nodes without exact module and `/go.mod` checksums in `go.sum` are not inventory
entries. Every group records exact versions, roles, accepted license families,
license-file paths, and SHA-256 hashes; graph-only license files are checked-in
bounded evidence while runtime/test files remain verified against the actual
module cache. Its artifact SHA-256 is `82be3ff6cd74533d468031fca8501bebcc04a1c9c8e9d25057b70f3e9514d51f`.

The inventory test compares the artifact with both derived closures, validates
each module's exact `go.sum` module and `/go.mod` checksums, and re-reads each
reviewed license file to verify its family and hash. Runtime/test entries also
require their module-cache directory; graph-only entries require their checked-in
evidence file instead. The repository-wide module file is not an inventory
authority: modules such as `modernc.org/sqlite` remain outside this record when
they are absent from the adapter closure.

This is an accepted supply-chain cost: three direct modules, sixteen runtime
transitive modules, and four module-graph-only modules, with no test-only adapter
dependencies and no cgo dependency introduced by the renderer.

## Fallback and non-decisions

The fallback remains `gdamore/tcell` v3 if Bubble Tea proves library-specific
unfit. That fallback requires a new evidence-backed decision or a reopening of
this record; it is not an invitation to mix rendering stacks. No full launcher,
session launch, cross-Product query, S2/S3 implementation, or framework-driven
domain type is authorized by this spike.

## Reopen and falsifiers

Reopen CD-0014 if any of these is demonstrated by a reproducible test or
production evidence:

- the isolated adapter cannot preserve the launcher/read-port boundary;
- a required C14/C17 meaning is lost at 80 columns, without color, or through
  keyboard interaction;
- text input or resize handling requires a framework workaround that violates
  the explicit-event/no-autonomous-polling rule;
- renderer updates cause an unbounded or autonomous read stream;
- a supported Linux target cannot build or pass race-capable launcher tests;
- a material security, licensing, maintenance, or supply-chain defect appears;
- Product-only query scope fails an operator-approved launcher workflow, or an
  accepted cross-Product query contract supersedes it.

Screen-reader and other assistive-technology validation is deferred to launcher
implementation acceptance. Once that validation is defined and run, a failure is
also a CD-0014 falsifier.

When a library-specific falsifier is established, evaluate the tcell v3 fallback
against the same hard proofs. Otherwise preserve Bubble Tea and fix the owning
adapter or contract rather than adding a second renderer path.
