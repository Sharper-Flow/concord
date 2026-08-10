# Terminal launcher — candidate

**Status:** Non-authorizing candidate answer to open clarification C18.

This document proposes a shape. It binds nothing, authorizes no implementation, and
does not narrow any accepted contract. It becomes authority only if the operator
accepts it, at which point it takes the accepted-contract form used by
[`product-row-contract.md`](./product-row-contract.md).

One question inside it — the terminal rendering dependency — cannot be settled by a
candidate at this layer and is surfaced in §11 as a conflict with the current
no-third-party-dependency rule rather than silently decided.

## 1. The gap

[`priorities.md`](./priorities.md) makes the Product-first terminal launcher the
primary operator surface, and [`rollout-plan.md`](./rollout-plan.md) §3 makes
Product-first visibility part of the replacement-ready floor. R1 in
[`clarifications.md`](./clarifications.md) confirms the launcher is the daily
operating surface and that the predecessor session-bootstrap layer is not a candidate
for it.

Two accepted or candidate contracts describe things the launcher renders:

- accepted C14 ([`product-row-contract.md`](./product-row-contract.md)) fixes the
  Product row;
- candidate C17 proposes the Product coordination drill-down. It is not yet merged;
  this document links it once C17 lands.

Neither describes the launcher. C14 §Status is explicit that it "does not decide"
terminal interaction, keybindings, layout toolkit, the Product detail screen, or the
component tree. C17 §4 defers to the launcher as an already-accepted container. No
accepted document specifies the container itself.

The concrete consequence: there is today no accepted answer to what screens exist,
how the operator moves between them, what actions the launcher may take, when it
re-reads state, or what establishes the ambient Product context that
[`design-constraints.md`](./design-constraints.md) §13 requires agents to inherit
structurally. This candidate proposes that container.

## 2. What this candidate decides

| Decides | Does not decide |
|---|---|
| The screen set and the navigation graph between them | Field sets inside C14 rows or C17 modes |
| What establishes and changes ambient Product context | The TS5 invocation-envelope mechanics that carry it |
| The interaction model and default keymap | Visual theme, colour palette, or box-drawing style |
| The narrow action surface and its boundary with native systems | Workflow semantics, which CD-0013 owns |
| The refresh model and its no-polling discipline | The bounded-check mechanics CD-0006 R3 already fixes |
| Read bounds and latency budget per screen | Query contracts, which PM1 owns |
| Failure, degradation, and first-run states | Recovery procedure, which PM10 owns |

## 3. Screen model

Four screens. The set is closed: a fifth screen requires a named operator job and
prototype evidence, on the same rule C14 §11 applies to row fields.

| # | Screen | Job | Body |
|---|---|---|---|
| S1 | Portfolio | Choose which Product becomes ambient context | C14 rows, paged |
| S2 | Product | Coordinate within one Product — what is blocked, what blocks it, what is next | C17 modes, if accepted |
| S3 | Work | Understand and act on one work item | Work detail, workflow step, evidence, approvals |
| S4 | Knowledge | Read durable Product knowledge for the ambient Product | Q9 search, Q10 note resolution |

### Navigation graph

```text
S1 Portfolio ──select──> S2 Product ──select──> S3 Work
     ^                        │  ^                  │
     └────────back────────────┘  └──────back────────┘
                              │
                              └──knowledge──> S4 Knowledge ──back──> S2
```

Rules:

- S1 is the entry screen and the only screen reachable without an ambient Product.
- Navigation is a stack. Back returns to the previous screen with its prior selection
  and scroll position restored; it never silently re-enters at the top.
- S3 is reachable only through S2. A work item is never addressed outside its Product,
  which keeps ambient context structural rather than a parameter (§4).
- S4 is scoped to the ambient Product. There is no global cross-Product knowledge
  browse, matching the query contract's deferral of cross-Product prioritization.
- R4 in [`clarifications.md`](./clarifications.md) fixes Product → component as the
  durable-knowledge navigation path; S4 renders that path, and workflows and changes
  appear as linked history from the component view rather than as a top-level browse.

R5 applies to every screen: active gates and active problems render first, and
completed history is reachable only by explicit drill-down.

## 4. Ambient Product context

[`design-constraints.md`](./design-constraints.md) §13 requires that Product context
is ambient rather than a parameter threaded through calls, and that spawned workers
inherit it structurally. The launcher is where the operator sets it.

| Aspect | Proposed rule |
|---|---|
| What establishes it | Selecting a Product row on S1 and entering S2 |
| What changes it | Returning to S1 and selecting a different Product; nothing else |
| Scope | Exactly one ambient Product at a time per launcher instance |
| Visibility | The ambient Product is displayed persistently on S2, S3, and S4 |
| Propagation | Handed to a launched session as resolved scope, re-resolved by the core on every call per TS5 |
| Inheritance | Workers spawned inside that session inherit it structurally, never by prompt convention |
| Absence | On S1 there is no ambient Product, and no action requiring one is offered |

Two independent launcher instances may hold two different ambient Products; each is
a view over the same single global SQLite authority per PM2, and neither caches
authority. There is no ambient-context file, environment variable, or shell state
that a second process could read as a second authority.

The predecessor's explicit path-confirmation plumbing is the recorded anti-pattern
here. Ambient context is never re-confirmed by asking the operator to restate a path.

## 5. Interaction model

**Principles.** Keyboard is sufficient for every action. Mouse may be supported but
is never required. Navigation keys are uniform across all four screens. No action
that changes durable state is bound to a single unconfirmed keystroke.

**Proposed default keymap.**

| Key | Action | Scope |
|---|---|---|
| `j` / `k`, `↓` / `↑` | Move selection | All |
| `Enter` | Open selection, descending one screen | S1, S2 |
| `Esc`, `h`, `←` | Back one screen | S2, S3, S4 |
| `g` / `G` | First / last row | All |
| `Ctrl-d` / `Ctrl-u` | Half-page down / up | All |
| `n` / `p` | Next / previous page | S1, S2 |
| `/` | Filter within the current screen's already-fetched result set | S1, S2, S4 |
| `Tab` | Switch mode within a screen — C17 relation tree ↔ ranked table | S2 |
| `r` | Explicit refresh (§7) | All |
| `K` | Open Knowledge for the ambient Product | S2, S3 |
| `?` | Help overlay listing the active keymap | All |
| `q` | Quit; on S1 exits, elsewhere behaves as back | All |

`/` filters; it does not re-query. Filtering a bounded page is a display operation
over data already fetched under §9's bounds, so it cannot silently widen a read or
change ordering. When a filter hides rows, the count of hidden rows is shown — a
filtered view never presents itself as a whole result, matching C17's no-silent-
truncation rule.

The help overlay is generated from the active keymap rather than maintained as prose,
so a keymap change cannot drift from its documentation.

## 6. Action surface

C14 §1 and [`workflows.md`](./workflows.md) restrict the launcher to context-rich
navigation with narrow routing actions. This candidate makes that concrete.

**Permitted actions.**

| Action | Screen | Effect |
|---|---|---|
| Open | S1, S2 | Navigate; no durable write |
| Launch | S2, S3 | Hand ambient Product context to the session bootstrap layer and start or attach a session |
| Start | S3 | Begin the offered workflow action on the selected work item |
| Resume | S3 | Continue an interrupted workflow action |
| Approve / reject | S3 | Answer a pending approval challenge, when the operator holds the required authority |

**Excluded from every screen.**

- creating, editing, retitling, reprioritizing, or deleting work;
- editing specs, decisions, notes, or any durable knowledge;
- editing relations, membership, or resource records;
- any git, build, deploy, or external-system execution;
- bulk or multi-select operations of any kind.

Start, resume, and approve are the launcher's only durable writes, and each is a
dispatch into the accepted CD-0005 mutation surface — the launcher holds no second
mutation path, no direct store access, and no local workflow logic. Everything
excluded above is either drill-down editing that CD-0006 D6 places inside the
Product/workflow detail, or native-system execution that CD-0006 leaves with the
native system.

**Approval is an open question.** [`priorities.md`](./priorities.md) Priority 2
requires human authority at consequence boundaries, and C14 counts
`approval_required` on the row, so the operator is told an approval is pending in the
launcher. Whether the launcher is also where the operator *answers* it, or whether it
only routes to the surface that does, is listed in §14 for operator direction. This
candidate proposes answering it on S3, because routing the operator out of the
launcher to approve makes the launcher a notifier rather than an operating surface.

**Blocked actions.** When reliance state blocks execution (C14 §3), start, resume,
approve, and launch are refused with the typed reason. The refusal is visible before
the keystroke, not after it: an unavailable action renders as unavailable rather than
failing on invocation.

## 7. Refresh model

CD-0006 R3 and [`design-constraints.md`](./design-constraints.md) §2 forbid polling,
timers, and heuristic blocking authority. The launcher therefore has no refresh loop,
no background thread, and no interval.

| Trigger | Behaviour |
|---|---|
| Screen entry | One bounded read for that screen |
| Explicit `r` | Re-run that screen's read |
| After a launcher-initiated write | Re-run the affected screen's read, once |
| Everything else | No read |

Between reads the screen is a snapshot, and it says so. Every screen renders the
authority watermark and the observed-at age of its data, so a stale screen can never
look current — this is C14 §4's reliance discipline applied to the container rather
than the row.

A snapshot older than the accepted staleness bound renders its reliance state as
stale and blocks execution per §6, without the launcher inventing a freshness
judgement of its own. Whether the operator must explicitly refresh before a
consequential action, or whether the pre-action bounded check that CD-0006 R3 already
requires is sufficient, is listed in §14.

## 8. Rendering constraints

The launcher inherits C14 §4 wholesale and adds container-level rules.

- No horizontal scrolling is required on any screen at 80 columns.
- Narrow terminals may reflow to a second line but may not drop fields or hide
  meaning behind a mode.
- `degraded`, `unreachable`, `stale`, and `approval_required` never depend on colour
  alone; each carries a stable textual symbol.
- The screen is legible with colour disabled and via screen reader.
- Every screen shows, in fixed positions: the ambient Product, the authority
  watermark and data age, and the active-key hint line.
- Redraw is idempotent — two renders over unchanged state produce identical output,
  which makes both prototype acceptance and screenshot diffing meaningful.

## 9. Read path and bounds

| Screen | Read | Bound |
|---|---|---|
| S1 | One Product-row projection query, per C14 §8 | Page default 20, maximum 100 |
| S2 | One bounded query per mode, per C17 §6 | Q8 depth ≤ 3; Q5 paged by limit and cursor |
| S3 | One work-detail read | Single work item plus bounded workflow state |
| S4 | Q9 search, or Q10 note resolution | Paged; bounded result size |

- No screen issues per-row or per-work fan-out.
- P99 ≤ 100 ms locally at 10× measured dataset, matching the PM1 implementation
  target carried by C14 §8.
- All reads go through the accepted CD-0005 read tools or their in-process Go
  equivalent against the same typed projections and watermark. The launcher does not
  hold a second read path with different semantics.

## 10. Failure, degradation, and first run

| State | Proposed rendering |
|---|---|
| Authority unreachable | Typed unreachable screen; no cached rows presented as current; navigation into S2/S3/S4 refused with the reason |
| Partial coverage | The affected group renders `unavailable` with a typed reason and bounded omissions; never zero, never a shorter list presented as whole |
| Authoritative empty portfolio | Explicit authoritative-empty state distinguishable from unreachable |
| No Product has any actionable work | Authoritative-empty per C14's `focus_absent_reason`, not an error |
| First run, no database | Typed first-run state naming the initialization step; the launcher does not silently create authority as a side effect of being opened |
| Invariant violation, such as a relation cycle | Surfaced as `invariant_violation` per C17; never hidden and never auto-repaired |

Nothing in this table permits a repair action. Non-destructive recovery is PM10's,
and [`design-constraints.md`](./design-constraints.md) §19 forbids the launcher from
hand-repairing state it merely displays.

## 11. Implementation boundary — unresolved dependency conflict

The launcher is Go, in the Concord core, per R6 and
[`core-architecture.md`](./core-architecture.md) §1. The adapter boundary is
unaffected: TS6 keeps `concord.ts` a transport-only module, and the launcher is not
part of it.

The rendering dependency is a genuine conflict that this candidate surfaces rather
than resolves. `AGENTS.md` states that third-party Go dependencies require applicable
accepted issue or decision scope. The repository already carries one direct
dependency, `modernc.org/sqlite`, accepted under CD-0002 for the pure-Go binding, so
the rule is a decision gate rather than a prohibition. A full-screen interactive TUI
needs raw-mode terminal control, which the Go standard library does not provide; the
options differ in how much is taken on.

| Option | Shape | Cost | Risk |
|---|---|---|---|
| Standard library plus `golang.org/x/sys` | Hand-rolled raw mode, ANSI output, input parsing, resize handling | Highest implementation cost | Terminal-compatibility burden becomes Concord's, permanently |
| `gdamore/tcell` v3 | Cell-based rendering primitive, pure Go, no cgo | Moderate — rendering solved, widgets are not | v3 is recent and breaking relative to v2; `rivo/tview` has not adopted it |
| `charmbracelet/bubbletea` v2 plus `bubbles` | Framework with table, viewport, list, help, and key components | Lowest implementation cost | Largest dependency surface; v2 moved to a new module path |

`golang.org/x/sys` is already an indirect dependency, so the first option's true
addition is implementation burden rather than supply-chain surface. The third option's
component list maps closely onto §3's screens and §5's keymap, which is why it is the
cheapest path to a prototype.

This candidate takes no position. The choice is a decision-record question because it
is durable, hard to reverse once screens are written against a framework's model, and
governed by an existing rule. It is listed in §14.

## 12. Anti-requirements

Each is proposed as a prohibition because the pattern produces output that looks
authoritative while being non-derivable, unstable, or a second authority.

1. **No background refresh.** No timer, no poll, no watcher, no interval. Reads
   happen on the triggers in §7 and nowhere else.
2. **No cached authority.** The launcher holds no durable state of its own. Screen
   state is a render snapshot, discarded on exit.
3. **No second read or write path.** Every read and every write goes through the
   accepted tool surface against the same projections and watermark.
4. **No computed or inferred ordering.** Ordering comes from the stored explicit
   priority rank and the accepted query contracts, never from a model-assigned score,
   activity recency, or a heuristic.
5. **No repair actions.** Nothing in the launcher mutates state to fix what it
   displays.
6. **No dashboard drift.** Terminal counts, repository paths, velocity, percent
   complete, estimates, owners, and assignments stay excluded, consistent with C14 §5
   and C17 §5.
7. **No mouse dependency.** Every action is reachable by keyboard.
8. **No hidden meaning.** Nothing meaningful is conveyed by colour alone, by a mode
   the operator must discover, or by a field only visible at wide terminal widths.
9. **No cross-Product action surface.** The launcher acts within exactly one ambient
   Product, matching the query contract's cross-Product deferral.

## 13. Proposed acceptance tests

A prototype would need to satisfy at minimum:

- Selecting a Product on S1 establishes ambient context, and a session launched from
  S2 resolves the same Product without any path being restated.
- Two launcher instances hold two different ambient Products without either observing
  the other's selection.
- Back from S3 returns to S2 with the prior selection and scroll position intact.
- A work item cannot be reached without an ambient Product.
- Every action in §6 is reachable by keyboard alone, and the help overlay matches the
  active keymap exactly.
- With reliance state blocking execution, start, resume, approve, and launch render as
  unavailable before invocation, with the typed reason visible.
- With the authority unreachable, no screen renders cached rows as current.
- Partial coverage renders `unavailable` with a typed reason, never zero.
- An authoritative-empty portfolio is visually distinguishable from an unreachable one.
- First run with no database renders the typed first-run state and creates nothing.
- No read is issued between two consecutive `r` presses with no navigation in between.
- Two renders over unchanged state are byte-identical.
- Every screen renders without horizontal scroll at 80 columns, and preserves reliance
  and action meaning with colour disabled and via screen reader.
- S1 at 100 Products and S2 at maximum relation depth stay within §9's latency bound.

Operator test: from a cold start, identify the Product needing attention, enter it,
name what is blocked and what blocks it, and launch a session against the next work
item — without leaving the launcher and without restating any path. Failure reopens
the screen set or the action surface; it does not authorize adding a dashboard.

## 14. Questions for operator direction

1. **Rendering dependency (§11).** Standard library, `tcell`, or `bubbletea`. This is
   the one item here that plausibly needs its own decision record, because it is
   durable, costly to reverse, and gated by an existing dependency rule.
2. **Approval answering (§6).** Does the operator answer approval challenges on S3, or
   does the launcher only surface them and route elsewhere?
3. **Pre-action freshness (§7).** Is an explicit refresh required before a
   consequential action, or is CD-0006 R3's bounded check sufficient on its own?
4. **Launch boundary (§6).** How much does the launcher hand to the session bootstrap
   layer — resolved Product scope only, or also the selected work item as the
   session's starting subject?

## 15. Sequencing

This candidate depends only on accepted contracts plus candidate C17, so it does not
block on new law beyond question 1 in §14. It does depend on runtime that does not
exist yet. A practical order:

1. The workflow engine ships, so S3 has workflow state to render.
2. The rendering-dependency question is decided.
3. S1 renders C14 rows against real storage.
4. S2 renders C17 modes, and S3 renders work detail with the §6 action surface.
5. S4 follows, as the last screen and the least load-bearing.

S1 alone is a useful prototype for the §13 reliance, latency, and accessibility tests
before S2 exists. It is not a replacement-ready slice, and
[`priorities.md`](./priorities.md) §First-usable floor forbids calling it one.

## 16. Risks

| Risk | Proposed mitigation |
|---|---|
| The screen set accretes until the launcher becomes the dashboard C14 rejected | The set is closed in §3; a fifth screen requires a named operator job plus prototype evidence |
| A framework's model leaks into domain code, making the rendering choice hard to reverse | Screens consume the accepted read contracts only; no domain type is defined in terms of a rendering library |
| The no-polling rule makes the launcher feel stale in practice | Watermark and age are always visible, and explicit refresh is one keystroke; if this proves insufficient, the correct fix is a push notice mechanism under CD-0006 R3, never a poll |
| The action surface grows until the launcher becomes an editor | §6 excludes editing structurally, and every write is a dispatch into the accepted mutation surface |
| Latency degrades as screens gain content | The per-screen bound in §9 is an acceptance test, not an aspiration |

## 17. Falsifiers

This candidate should be revised or withdrawn when:

- the operator cannot complete the §13 operator test without leaving the launcher;
- four screens prove to be the wrong cut, in either direction;
- ambient context proves insufficient and callers genuinely need to pass explicit
  scope, which would reopen `design-constraints.md` §13 rather than this document;
- the no-polling refresh model proves unusable in daily operation and a push notice
  mechanism is required;
- the narrow action surface forces the operator out of the launcher so often that the
  launcher is a viewer rather than an operating surface; or
- the chosen rendering dependency cannot meet the accessibility or latency clauses.

## 18. Evidence basis

- Product-first terminal launcher is the primary operator surface, and Priority 3
  requires bounded, fast, Product-scoped reads with reviewed staleness
  ([`priorities.md`](./priorities.md) §§Operating envelope, 3–4).
- R1, R4, R5, and R6 fix the launcher's role, the Product → component navigation path,
  active-work-first defaults, and Go ownership
  ([`clarifications.md`](./clarifications.md)).
- C14 fixes the row and explicitly defers interaction, keybindings, layout toolkit,
  and the detail screen ([`product-row-contract.md`](./product-row-contract.md)).
- C17 proposes the Product drill-down and defers the container to the launcher
  (candidate, not yet merged).
- Ambient Product context, typed degradation, no-polling impact propagation, bounded
  reads, and non-destructive recovery are constraints §13, §2, §5, and §19
  ([`design-constraints.md`](./design-constraints.md)).
- The read and mutation surfaces the launcher dispatches into are accepted TS3/TS4
  under CD-0005 ([`agent-read-tool-contract.md`](./agent-read-tool-contract.md),
  [`agent-mutation-tool-contract.md`](./agent-mutation-tool-contract.md)).
- Go TUI dependency options, current as of 2026-08: `gdamore/tcell` v3 is released and
  breaking relative to v2, with `rivo/tview` not yet adopted
  (<https://github.com/gdamore/tcell>, <https://github.com/rivo/tview/issues/1145>);
  `charmbracelet/bubbletea` v2 publishes a component set covering tables, viewports,
  lists, help, and key maps (<https://github.com/charmbracelet/bubbletea>).
