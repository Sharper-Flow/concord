# CD-0014: terminal launcher rendering stack

**Status:** Accepted
**Approval date:** 2026-08-10
**Approval:** Operator-approved architecture spike under GitHub issue #39
**Supersedes:** The rendering-dependency and query-scope sub-questions in C18
**Implementation status:** This decision accepts the spike boundary only; launcher
screens and read-port wiring remain unbuilt.

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

The initial launcher query is **Product-only**: it is scoped to the ambient
Product and never searches across Products. The launcher remains read-only and
does not answer approvals, mutate durable state, derive workflow position, or
create a second state authority.

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
    production closure, and module-graph-only modules by traversing `go mod graph`
    from the exact direct Charm roots and intersecting the selected module versions.
    Offline module metadata locates every cached module's accepted license file,
    verifies actual license text and SHA-256 against the reviewed evidence, and
    compares all three exact sets with the strict machine-readable inventory
    artifact below.

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
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go mod graph
```

## Dependency and license inventory

The single reviewed source is the strict machine-readable
[`CD-0014 terminal-launcher dependency inventory`](./CD-0014-terminal-launcher-dependencies.v1.json).
It records 19 runtime modules, zero test-only modules, and 4 module-graph-only
modules. The runtime and test-only groups come from the two offline adapter
closures. The module-graph-only group contains selected module versions reachable
by traversing `go mod graph` from the exact direct Charm roots but absent from both
closures. These are fetched/checksummed module metadata not linked into the
launcher binary or test closure. Graph nodes that expose only go.mod metadata
without fetched content/checksums are not inventory entries. Every group records exact versions, roles,
accepted license families, license-file paths, and SHA-256 hashes. Its artifact
SHA-256 is `28458e7bf509cc5bd4e6de0896489c1f9569514036387259ad2447d5e2e3adf1`.

The inventory test compares the artifact with both derived closures, validates
each module's `go.sum` checksum and module-cache directory, and re-reads each
reviewed license file to verify its family and hash. The repository-wide module
file is not an inventory authority: modules such as `modernc.org/sqlite` remain
outside this record when they are absent from the adapter closure.

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
