# Terminal launcher — accepted contract

**Status:** Accepted under [`CD-0014`](./decisions/CD-0014-terminal-launcher-rendering.md),
amended by CD-0041.
**Implementation status:** S1 portfolio wiring shipped through issue #45 and PR #48.
Issue #51 implements the S2 Product coordination view, S3 Work detail, scoped
knowledge/search, explicit refresh, and identity-only OpenCode handoff. The
replacement-ready floor remains unclaimed; this slice is not replacement-ready.

**CD-0041 implementation gap:** issue #51's shipped S2/S3 views predate canonical
Domains. The amended S2 contract below requires Product → Domain navigation,
current Domain law, typed architecture relations, and unresolved overlap. That
runtime work remains outstanding and no floor satisfaction is inferred here.

This document is the accepted C18 launcher contract. CD-0014 records the rendering
spike, exact dependency inventory, evidence gate, and Product-only query scope.

## Context

The launcher is the operator's single entry surface. The binding inputs are the
C14 Product-row contract, the C17 coordination view, CD-0014 for rendering, and
the accepted priorities. This record decides the container the operator
actually runs: the three-screen model, ambient Product context, the interaction
model, the action surface, the refresh model, rendering constraints, the read
path and its bounds, failure and first-run behavior, and the accepted Bubble
Tea dependency.

## Contract

The binding contract is sections 1 through 13: the gap and the launcher's job,
the screen model, ambient Product context, the interaction model, the action
surface, the refresh model, rendering constraints, the read path and bounds,
failure and first-run behavior, the implementation boundary, the
anti-requirements, and the acceptance tests. Section 14 records resolved
operator questions; sections 15 through 18 record sequencing, risks,
falsifiers, and sources, and carry no obligation.

## 1. The gap

[`priorities.md`](./priorities.md) makes the Product-first terminal launcher the
primary operator surface, and [`rollout-plan.md`](./rollout-plan.md) §3 makes
Product-first visibility part of the replacement-ready floor. R1 in
[`clarifications.md`](./clarifications.md) confirms the launcher is the daily
operating surface and that the predecessor session-bootstrap layer is not a candidate
for it.

Two accepted contracts describe things the launcher renders:

- accepted C14 ([`product-row-contract.md`](./product-row-contract.md)) fixes the
  Product row;
- accepted C17 defines the Product coordination drill-down in
  [`product-coordination-view.md`](./product-coordination-view.md).

Before C18, neither described the launcher. C14 §Status is explicit that it "does not
decide" terminal interaction, keybindings, layout toolkit, the Product detail screen,
or the Domain hierarchy. C17 §4 defers to the launcher as an already-accepted container.
No accepted document specified the container itself.

The gap was that no accepted answer specified what screens exist, how the operator
moves between them, what the launcher may do, when it re-reads state, or what
establishes the ambient Product context that
[`design-constraints.md`](./design-constraints.md) §13 requires agents to inherit
structurally. This contract defines that container.

## 2. The launcher's job

**Operator direction, 2026-08-09:** the launcher exists to see status and to resume
work in the OpenCode TUI. Nothing else.

That direction is narrower than it may appear, and the narrowness is the design. It
makes the launcher **read-only by construction**: it performs no durable write, holds
no mutation path, and therefore cannot become a second write authority over state the
core owns. Approvals, edits, workflow transitions, and every other consequential
action happen inside the session the launcher starts, through the accepted CD-0005
mutation surface — not in the launcher.

Three consequences follow, and they shape the rest of this document:

1. §6's action surface collapses to navigation plus session launch.
2. §7's staleness handling becomes purely a display concern. A read-only surface has
   no consequential boundary to gate, so the launcher never blocks execution; it
   reports reliance state and lets the core enforce it where the action actually
   occurs.
3. §12 can prohibit durable writes outright rather than bounding them.

| Decides | Does not decide |
|---|---|
| The screen set and the navigation graph between them | Field sets inside C14 rows or C17 modes |
| Where durable knowledge renders | What durable knowledge contains, which PM6/PM7 and self-documentation own |
| What establishes and changes ambient Product context | The TS5 invocation-envelope mechanics that carry it |
| The interaction model and default keymap | Visual theme, colour palette, or box-drawing style |
| That the launcher hands identity, not intent, to the session | Workflow semantics, which CD-0013 owns |
| The refresh model and its no-polling discipline | The bounded-check mechanics CD-0006 R3 already fixes |
| Read bounds and latency budget per screen | Query contracts, which PM1 owns |
| Failure, degradation, and first-run states | Recovery procedure, which PM10 owns |

## 3. Screen model

Three screens. The set is closed: a fourth screen requires a named operator job and
prototype evidence, on the same rule C14 §11 applies to row fields.

| # | Screen | Job | Body |
|---|---|---|---|
| S1 | Portfolio | Choose which Product becomes ambient context | C14 rows, paged |
| S2 | Product | Navigate Domains and coordinate within one Product — what law governs, what overlaps, what is blocked, and what is next | Domain hierarchy/relations, current-law and overlap sections, accepted C17 work modes, plus bounded Product/Domain/Project knowledge |
| S3 | Work | Understand one work item and resume it in a session | Work detail, workflow position, evidence, plus the work-scoped knowledge section |

### Durable knowledge is a section, not a screen

**Operator direction, 2026-08-09, amended by CD-0041:** knowledge belongs to the
thing that owns it — Product, Domain, Project, Initiative, or work item — rather
than to a global browse surface.

So there is no knowledge screen. Each screen renders the knowledge scoped to the
entity it is already showing:

| Screen | Knowledge shown | Owner |
|---|---|---|
| S2 | Current law, decisions, notes, and architecture relations owned by the Product, selected Domain, and its Projects | Product / Domain / Project |
| S3 | Law pins, decisions, notes, and evidence attached to this work item | Work item, including Initiative and Product-changing kinds |

This is R4's Product → Domain navigation applied without a second path: knowledge
is reached by navigating to its owner, and workflows and changes appear as linked
history from that owner rather than as a top-level browse. It also holds locality —
a Domain's current law, changes, evidence, decisions, and runbooks stay near each other rather than being
re-aggregated into a global list that would need its own ordering rule.

Initiatives need no special screen. CD-0041 keeps Initiative an ordinary work-item
kind and secondary business/outcome context, so it renders on S3 like other work
without becoming the Domain browse path.

Knowledge search (Q9) and note resolution (Q10) belong in the launcher and are
covered by §5's query mode. The durable knowledge resolver has shipped, so the
launcher uses its bounded scoped read path.

### Knowledge is a bounded live section

**Accepted direction, 2026-08-10:** the knowledge section is written into the UI and
uses the shipped resolver through the accepted bounded read surface.

The reason is scope honesty. Durable Product knowledge is spread across decision
records, specifications, runbooks, PM6 canonical git notes, and ordinary repository
files of several types. Resolving "what knowledge belongs to this entity" across those
sources is a subsystem with its own placement, indexing, and retention rules — PM6 and
PM7 already govern parts of it — not a panel. The panel is cheap; the resolver is not.

The section has one live shape:

| Section | The section is | Behaviour |
|---|---|---|
| S2/S3 | Live and bounded | Renders resolved knowledge for the entity on screen through the accepted read surface; incomplete coverage is `unavailable` with a typed reason |

Three rules keep the section safe.

1. **Unavailable never renders as empty.** `unavailable` is distinct from
   authoritative-empty and from complete coverage. An operator must never be able to
   conclude from a partial read that an entity has no knowledge.
2. **The slot is bounded and stable.** The section obeys §8's rendering constraints
   and does not become a global browse surface.
3. **The read path is authoritative.** The section uses one bounded, entity-scoped
   read through the accepted TS3 surface; it does not introduce a launcher-side
   resolver or a second knowledge authority.

The decision that knowledge belongs to its owning Product, Domain, Project,
Initiative, or work item
remains binding; only coverage and typed degradation vary per read.

### Navigation graph

```text
S1 Portfolio ──select──> S2 Product/Domain ──select work──> S3 Work ──launch──> OpenCode session
     ^                        │                    │
     └────────back────────────┘   └─────back───────┘
                                  │
                          (knowledge renders in place on S2 and S3)
```

Rules:

- S1 is the entry screen and the only screen reachable without an ambient Product.
- Navigation is a stack. Back returns to the previous screen with its prior selection
  and scroll position restored; it never silently re-enters at the top.
- S3 is reachable only through S2. A work item is never addressed outside its Product,
  which keeps ambient context structural rather than a parameter (§4).
- Launch is a leaf, not a screen transition. It hands off to a session (§6) and the
  launcher remains on S3.

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
| Visibility | The ambient Product is displayed persistently on S2 and S3 |
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
is never required. Navigation keys are uniform across all screens.

The launcher performs no durable write (§2), so there is no destructive keystroke to
guard. Launch is the only key with an external effect, and it is recoverable by
closing the session.

**Proposed default keymap.**

| Key | Action | Scope |
|---|---|---|
| `j` / `k`, `↓` / `↑` | Move selection | All |
| `Enter` | Open selection, descending one screen | S1, S2 |
| `Esc`, `h`, `←` | Back one screen | S2, S3 |
| `g` / `G` | First / last row | All |
| `Ctrl-d` / `Ctrl-u` | Half-page down / up | All |
| `n` / `p` | Next / previous page | S1, S2 |
| `/` | Filter the current screen's already-fetched result set | S1, S2 |
| `s` | Query — submit a bounded semantic search and render its results | S2, S3 only; requires ambient Product |
| `Ctrl-l` | Clear the text input | All, while a filter or query input is open |
| `Tab` | Cycle panel focus within S2's answer stack (CD-0048); on S3 cycle sections — knowledge | S2, S3 |
| `l` | Launch a session for the current scope (§6) | S2, S3 |
| `r` | Explicit refresh (§7) | All |
| `?` | Help overlay listing the active keymap | All |
| `q` | Quit; on S1 exits, elsewhere behaves as back | All |

`Ctrl-l` is free to carry clear on this surface. The launcher owes the terminal no
explicit redraw, because the rendering framework owns redraw (§11), so the key's
conventional terminal meaning has no work to do here. `Ctrl-u`, the other common
clear-line convention, is unavailable: the table above binds it to half-page up.

The help overlay is generated from the active keymap rather than maintained as prose,
so a keymap change cannot drift from its documentation.

### Filter and query are two different things

The launcher accepts typed input for two purposes, and conflating them would hide a
read behind what looks like a display control.

| | Filter (`/`) | Query (`s`) |
|---|---|---|
| Effect | Narrows rows already on screen | Issues a bounded read |
| Scope | The already-fetched Product rows on S1, or the current page on S2/S3 | The ambient Product on S2/S3; unavailable on S1 |
| Ordering | Unchanged from the underlying contract | Set by the query contract it dispatches to |
| Cost | None | One bounded read, counted in §9 |
| Result | A subset, with the hidden-row count shown | A result set, paged, with typed coverage |

`/` cannot silently widen a read or change ordering, because it never re-queries.
When it hides rows, it shows how many — a filtered view never presents itself as a
whole result, matching C17's no-silent-truncation rule.

`s` is a read only after an ambient Product exists. On S2/S3 it obeys every read rule
in this document: bounded and paged per §9, dispatched through the accepted TS3
surface, carrying the same watermark and reliance state as any other screen, and
rendering `unavailable` with a typed reason rather than a short list presented as
complete. Query results are a view, not a new screen; `Esc` returns to the unfiltered
screen with prior selection intact. S1 has no `s` binding: its Product portfolio is
already fetched by the S1 entry/refresh read, and `/` only narrows those rows locally.

What is queryable follows what the screen owns: S1 filters fetched Products locally;
semantic query applies only to work within the ambient Product on S2/S3 and durable
knowledge through the shipped resolver (§3). Query remains Product-only and never
spans Products.

### Text entry is a first-class requirement

Filter and semantic query use a real text input: cursor movement, mid-string editing,
deletion, paste, and clear. This is the launcher's only input widget, and it is not optional —
it raises the floor for the rendering-dependency choice in §11 above what a
navigate-only surface would need.

Two constraints keep it honest. Text entry never mutates durable state; S1 filter input
selects only what to show, while S2/S3 query input selects what to read. Neither selects
what to write. A semantic query in flight renders as in flight — a stale result set
never sits under a newer query string as though it answered it.

## 6. Action surface

The launcher navigates and launches. That is the whole surface.

| Action | Screen | Effect | Durable write |
|---|---|---|---|
| Open | S1, S2 | Navigate one screen down | None |
| Back | S2, S3 | Navigate one screen up | None |
| Filter | S1, S2 | Narrow the rows already on screen | None |
| Query | S2, S3 | Issue one bounded Product-scoped search and render its results | None |
| Refresh | All | Re-run the current screen's read | None |
| Launch | S2, S3 | Start or attach an OpenCode session carrying resolved scope | None by the launcher |

**Excluded from every screen.** Creating, editing, retitling, reprioritizing, or
deleting work; starting, transitioning, or completing a workflow; answering approval
challenges; editing specs, decisions, notes, or any durable knowledge; editing
relations, membership, or resource records; any git, build, deploy, or external-system
execution; and bulk or multi-select operations of any kind.

**Approvals are surfaced, not answered.** C14 counts `approval_required` on the
Product row and S3 shows the pending challenge, so the operator learns in the launcher
that a decision is waiting. Answering it happens in the session, through the accepted
CD-0005 mutation surface. The launcher is the place you find out; the session is the
place you decide.

### The launcher hands identity, not intent

**Operator direction, 2026-08-09:** what launch does depends on the current state of
the change or workflow, resolved by whichever side is cleaner.

The clean side is the session, and the reason is constitutional rather than
ergonomic. If the launcher inspected workflow state and handed the session a specific
intent — start fresh, resume at step N — it would hold its own derivation of where the
workflow is. [`design-constraints.md`](./design-constraints.md) §14 requires a single
derivation with no parallel authority, and R3 in
[`clarifications.md`](./clarifications.md) gives each workflow's durable orchestrator
sole ownership of its lifecycle record. A launcher-computed intent is exactly the
split-authority shape the predecessor postmortem identifies as the recurring root
cause.

So the handoff carries identity only:

| Launched from | Handoff |
|---|---|
| S2 | Resolved Product scope |
| S3 | Resolved Product scope plus the work item's stable ID |

The launcher starts the core-owned `concord session` bootstrap with that identity.
Per CD-0031, the child reads the canonical CD-0016 continuity projection, validates
its versioned packet, and gives the exact packet to OpenCode before the session starts.
State-dependent behaviour is preserved and resolved once by the authority that owns
it, at the moment of use rather than at the moment of display. The launcher model and
renderer never see the packet or derive workflow position.

This also makes the launcher's snapshot harmless. A stale screen can hand off a work
ID; it cannot hand off a stale opinion about workflow position, because it never forms
one.

The launcher offers one launch action whose availability does not vary with workflow
state. Labelling may reflect state for orientation — "resume" against in-progress
work, "open" otherwise — but the label is display text derived from the same read that
populates the screen, never a second decision.

## 7. Refresh model

CD-0006 R3 and [`design-constraints.md`](./design-constraints.md) §2 forbid polling,
timers, and heuristic blocking authority. The launcher therefore has no refresh loop,
no background thread, and no interval.

| Trigger | Behaviour |
|---|---|
| Screen entry | One bounded read for that screen |
| S1 entry | One bounded Product-row read for the portfolio |
| S2/S3 query submitted | One bounded Product-scoped read for that query |
| S1 filter submitted | No read; narrow the fetched Product rows locally |
| Explicit `r` | Re-run the current screen's read; on S1 this is the Product-row read |
| Everything else | No read |

S2/S3 semantic query reads on submit, never per keystroke. Incremental search would
turn one operator intent into an unbounded read stream, which §9's bounds and the
no-polling discipline both exclude; typing is free, submitting costs one read. S1
filter submission is always read-free because it never leaves the fetched portfolio.

Between reads the screen is a snapshot, and it says so. Every screen renders the
authority watermark and the observed-at age of its data, so a stale screen can never
look current — this is C14 §4's reliance discipline applied to the container rather
than the row.

**Staleness is displayed, never enforced here.** C14's `blocks_execution` remains
meaningful, but the launcher is not where it bites: a read-only surface has no
consequential boundary. The launcher renders the blocking reliance state so the
operator sees it, and the core enforces it at the action boundary inside the session,
under the bounded check CD-0006 R3 already requires. The launcher adds no freshness
judgement of its own and holds no second gate.

Launching against a stale screen is therefore safe by construction. The session
re-resolves scope and state on every call per TS5; nothing the launcher displayed is
carried into the session as authority.

## 8. Rendering constraints

The launcher inherits C14 §4 wholesale and adds container-level rules.

- No horizontal scrolling is required on any screen at 80 columns.
- Narrow terminals may reflow to a second line but may not drop fields or hide
  meaning behind a mode.
- `degraded`, `unreachable`, `stale`, and `approval_required` never depend on colour
  alone; each carries a stable textual symbol.
- The spike proves color-independent textual semantics and keyboard reachability.
  Screen-reader and other assistive-technology validation is deferred to launcher
  implementation acceptance.
- Every screen shows, in fixed positions: the ambient Product, the authority
  watermark and data age, and the active-key hint line.
- Redraw is idempotent — two renders over unchanged state produce identical output,
  which makes both prototype acceptance and screenshot diffing meaningful.

## 9. Read path and bounds

| Screen | Read | Bound |
|---|---|---|
| S1 | One Product-row projection query, per C14 §8 | Page default 20, maximum 100 |
| S2 | One bounded query per mode, per C17 §6, plus one Product-scoped knowledge read | Q8 depth ≤ 3; Q5 paged by limit and cursor; knowledge list paged |
| S3 | One work-detail read, plus one work-scoped knowledge read | Single work item plus bounded workflow state; knowledge list paged |
| Query (S2/S3 only) | One bounded read per submitted query, scoped to the ambient Product | Paged by limit and cursor; typed coverage; never unbounded |

- The knowledge reads above use the shipped resolver. They remain one bounded read
  for the entity already on screen, with typed coverage and no per-row fan-out.
- No screen issues per-row or per-work fan-out. The knowledge section is one bounded
  read for the entity already on screen, not a read per row.
- The knowledge section is read when it is first focused on that screen entry, never
  again on that entry. What that costs differs by screen, because the section is not
  focusable in the same way on both:
  - **S2:** knowledge is part of the Domain panel, and the Domain panel is focused on
    entry. First-focused and on-entry are therefore the same moment, and entering S2
    costs two reads — the Domain read and the Product-scoped knowledge read. Panel
    focus cycles Domain → blocked → next and never lands on a separate knowledge
    section, so there is no later moment to defer the read to.
  - **S3:** knowledge is its own focusable section, cycled after Domains, relations,
    and ranked work. It is read when the operator first focuses it, so entering S3
    costs one read and an operator who never opens the section never pays for it.
- The rule exists so the common status-checking path does not pay for queries it does
  not use. S1 issues no knowledge read at all, and S3 defers its read until asked. Two
  reads is the floor for S2, which renders knowledge in the panel it opens on.
- P99 ≤ 100 ms locally at 10× measured dataset, matching the PM1 implementation
  target carried by C14 §8.
- All reads go through the accepted CD-0005 read tools or their in-process Go
  equivalent against the same typed projections and watermark. The launcher does not
  hold a second read path with different semantics.

## 10. Failure, degradation, and first run

| State | Proposed rendering |
|---|---|
| Authority unreachable | Typed unreachable screen; no cached rows presented as current; navigation into S2/S3 refused with the reason |
| Partial coverage | The affected group renders `unavailable` with a typed reason and bounded omissions; never zero, never a shorter list presented as whole |
| Authoritative empty portfolio | Explicit authoritative-empty state distinguishable from unreachable |
| No Product has any actionable work | Authoritative-empty per C14's `focus_absent_reason`, not an error |
| Knowledge coverage unavailable or incomplete | Typed `unavailable` with a stable reason and bounded omissions; never presented as authoritative-empty or as complete |
| Entity has no durable knowledge | Authoritative-empty knowledge section, distinguishable from an unread one |
| First run, no database | Typed first-run state naming the initialization step; the launcher does not silently create authority as a side effect of being opened |
| Invariant violation, such as a relation cycle | Surfaced as `invariant_violation` per C17; never hidden and never auto-repaired |

Nothing in this table permits a repair action. Non-destructive recovery is PM10's,
and [`design-constraints.md`](./design-constraints.md) §19 forbids the launcher from
hand-repairing state it merely displays. A read-only launcher cannot repair anything
by construction, which is the point.

## 11. Implementation boundary — accepted dependency choice

The launcher is Go, in the Concord core, per R6 and
[`core-architecture.md`](./core-architecture.md) §1. The adapter boundary is
unaffected: TS6 keeps `concord.ts` a transport-only module, and the launcher is not
part of it.

The rendering dependency was a genuine conflict while C18 was open. CD-0014 resolves
it: the accepted issue and decision scope permit the isolated Bubble Tea v2 adapter.
A full-screen interactive TUI needs raw-mode terminal control, which the Go standard
library does not provide; the spike selected the lowest-cost widget path that passed
the hard proofs.

| Option | Shape | Cost | Risk |
|---|---|---|---|
| Standard library plus `golang.org/x/sys` | Hand-rolled raw mode, ANSI output, input parsing, resize handling | Highest implementation cost | Terminal-compatibility burden becomes Concord's, permanently |
| `gdamore/tcell` v3 | Cell-based rendering primitive, pure Go, no cgo | Moderate — rendering solved, widgets are not | v3 is recent and breaking relative to v2; `rivo/tview` has not adopted it |
| `charmbracelet/bubbletea` v2 plus `bubbles` | Framework with table, viewport, list, help, and key components | **Selected by CD-0014** | Largest dependency surface; v2 moved to a new module path |

`golang.org/x/sys` is already an indirect dependency, so the first option's true
addition is implementation burden rather than supply-chain surface. The third option's
component list maps closely onto §3's screens and §5's keymap, which is why it is the
cheapest path to a prototype.

The read-only narrowing in §2 removes forms, modals, and confirmation dialogs, but it
does not make this a display-only surface. §5 requires a real text input for filter and
query, with cursor movement, mid-string editing, paste, and clear. The widget floor is
therefore tables, a scrollable pane, a key map, and one text input — modest, but past
the point where hand-rolled input parsing is incidental work. Input handling is also
where terminal compatibility gets awkward: key encodings, bracketed paste, and resize
during editing are exactly the cases the standard-library option would take on
permanently.

The choice is durable and is governed by CD-0014. The renderer remains isolated so a
library-specific falsifier can move the adapter to tcell v3 without changing the
framework-independent launcher model or read port.

## 12. Anti-requirements

Each is proposed as a prohibition because the pattern produces output that looks
authoritative while being non-derivable, unstable, or a second authority.

1. **No durable writes.** The launcher never mutates state. It holds no mutation
   path, no direct store access, and no local workflow logic.
2. **No background refresh.** No timer, no poll, no watcher, no interval. Reads
   happen on the triggers in §7 and nowhere else.
3. **No cached authority.** The launcher holds no durable state of its own. Screen
   state is a render snapshot, discarded on exit.
4. **No second read path.** Every read goes through the accepted tool surface against
   the same projections and watermark.
5. **No derived workflow position.** The launcher never computes where a workflow is
   or what should happen next; it hands identity and lets the owning authority
   resolve state (§6).
6. **No computed or inferred ordering.** Ordering comes from the stored explicit
   priority rank and the accepted query contracts, never from a model-assigned score,
   activity recency, or a heuristic.
7. **No repair actions.** Nothing in the launcher mutates state to fix what it
   displays.
8. **No dashboard drift.** Terminal counts, repository paths, velocity, percent
   complete, estimates, owners, and assignments stay excluded, consistent with C14 §5
   and C17 §5.
9. **No mouse dependency.** Every action is reachable by keyboard.
10. **No hidden meaning.** Nothing meaningful is conveyed by colour alone, by a mode
    the operator must discover, or by a field only visible at wide terminal widths.
11. **No cross-Product action surface.** The launcher views one ambient Product.
     Query is Product-only and no result set spans Products (§14). This bounds
     result sets, not reach: S1 lists every Product in the portfolio, so all
     Products remain reachable without leaving the launcher. CD-0021 records that
     distinction as the reading of floor condition 1.
12. **No incremental query.** An S2/S3 semantic query reads on submit. Keystrokes
    never issue reads; S1 has no semantic-query binding.
13. **No filter that queries, and no query that pretends to be a filter.** S1 `/`
    narrows fetched Product rows with zero reads; S2/S3 `s` is a distinct
    Product-scoped semantic read. The two modes are distinguishable on screen, and a
    result set is never presented as though it were the unfiltered whole.
14. **No unavailable coverage that reads as data.** Incomplete knowledge coverage
     renders its typed reason and omissions; it never becomes an empty result, zero
     count, or blank pane.

## 13. Acceptance tests

A prototype would need to satisfy at minimum:

- Selecting a Product on S1 establishes ambient context, and a session launched from
  S2 resolves the same Product without any path being restated.
- Two launcher instances hold two different ambient Products without either observing
  the other's selection.
- Back from S3 returns to S2 with the prior selection and scroll position intact.
- A work item cannot be reached without an ambient Product.
- Launching from S3 hands the work item's stable ID and nothing about workflow
  position; the core session bootstrap supplies a generated, digest-bound continuity
  packet before OpenCode starts, and the session resumes at the durable current step.
- An unknown session type, session contract version, or manifest digest mismatch
  fails before OpenCode starts.
- Launching from a deliberately stale S3 snapshot resumes at the *current* step, not
  the displayed one.
- No durable write is observable in the event log across a full launcher session that
  navigates every screen and launches from both S2 and S3.
- Knowledge renders in place on S2 and S3 for the entity on screen, and no screen
  offers a cross-entity knowledge browse.
- The scoped knowledge section uses the shipped resolver, remains bounded, and
  distinguishes unavailable coverage from authoritative-empty knowledge.
- An Initiative work item renders on S3 with its knowledge section, with no Initiative-specific
  screen or code path.
- S2 renders the Product's bounded Domain hierarchy, current-law coverage, typed
  Domain relations, and unresolved architecture overlap before subordinate work
  modes; unavailable coverage never appears empty.
- The knowledge section is not read when the operator never focuses it.
- Every action in §6 is reachable by keyboard alone, and the help overlay matches the
  active keymap exactly.
- Typing an S2/S3 semantic query issues no read; submitting it issues exactly one.
- S1 has no `s` key binding; submitting `/` after editing the local Product filter
  issues zero reads and only narrows the fetched rows.
- A query result set renders its own coverage and reliance state, and incomplete
  coverage renders `unavailable` rather than a short list.
- `Esc` from a query result returns to the unfiltered screen with prior selection and
  scroll position intact.
- A filtered view shows the count of hidden rows and is distinguishable on screen from
  a query result set.
- The text input supports cursor movement, mid-string editing, deletion, paste, and
  clear, and no input path mutates durable state.
- A query submitted while an older result is displayed never leaves the stale result
  under the new query string without an in-flight indication.
- Blocking reliance state renders visibly on S1, S2, and S3 without the launcher
  refusing to launch.
- With the authority unreachable, no screen renders cached rows as current.
- Partial coverage renders `unavailable` with a typed reason, never zero.
- An authoritative-empty portfolio is visually distinguishable from an unreachable one.
- An entity with no knowledge is distinguishable from an unread knowledge section.
- First run with no database renders the typed first-run state and creates nothing.
- No read is issued between two consecutive `r` presses with no navigation in between.
- Two renders over unchanged state are byte-identical.
- The representative S1 render has no horizontal-scroll requirement at 80 columns and
  preserves reliance meaning with color-independent textual semantics. Screen-reader
  and assistive-technology validation is deferred to launcher implementation acceptance.
- S1 at 100 Products and S2 at maximum relation depth stay within §9's latency bound.

Operator test: from a cold start, identify the Product needing attention, enter
it, name the governing Domain and unresolved architecture overlap, name what is
blocked and what blocks it, and resume the next work item in a session — without
leaving the launcher and without restating any path. Failure reopens the
screen set or the handoff rule; it does not authorize adding a dashboard or a
mutation surface.

## 14. Questions for operator direction

**Resolved by CD-0014.**

1. **Rendering dependency (§11).** Bubble Tea v2 behind the isolated adapter;
   fallback tcell v3 if a library-specific falsifier is proven. Exact versions and
   evidence are binding in CD-0014.
2. **Query scope (§5).** Product-only, scoped to the ambient Product. Cross-Product
   query is excluded until a separate accepted contract demonstrates the need.

**Resolved 2026-08-09.**

| Question | Direction | Effect |
|---|---|---|
| Are approvals answered in the launcher? | No. The launcher shows status and resumes work in the OpenCode TUI | §2, §6 — launcher is read-only; approvals are surfaced and answered in the session |
| Is an explicit refresh required before consequential action? | Moot. A read-only launcher has no consequential boundary | §7 — staleness is displayed, never enforced by the launcher |
| Does a knowledge screen belong? | Knowledge belongs to its owning Product, Domain, Project, Initiative, or work item | §3 — no knowledge screen; a scoped section on S2 and S3 |
| Who resolves what launch does? | Whichever is cleaner | §6 — the session, because launcher-side resolution would be a second derivation of workflow position |

## 15. Sequencing

This accepted contract depends on accepted C14 plus accepted C17's bounded
read shapes. The workflow engine and durable knowledge resolver have shipped, so
neither is a stale sequencing prerequisite. A practical order is:

1. The selected renderer adapter is wired to the accepted read port.
2. S1 renders C14 rows against real storage.
3. S2 renders C17 modes; S3 renders work detail and the identity-only launch handoff.
   Scoped knowledge sections use the shipped resolver and remain bounded.

These are the launcher's first implementation round. Cross-Product query remains
outside this contract and requires its own issue and accepted evidence.

S1 alone is a useful prototype for the §13 reliance, latency, and accessibility tests
before S2 exists. It is not a replacement-ready slice, and
[`priorities.md`](./priorities.md) §First-usable floor forbids calling it one.

## 16. Risks

| Risk | Proposed mitigation |
|---|---|
| The screen set accretes until the launcher becomes the dashboard C14 rejected | The set is closed in §3; a fourth screen requires a named operator job plus prototype evidence |
| Read-only proves too narrow and the operator wants to act without opening a session | Reopen §2 deliberately, with the named action and evidence; do not add writes incrementally |
| A framework's model leaks into domain code, making the rendering choice hard to reverse | Screens consume the accepted read contracts only; no domain type is defined in terms of a rendering library |
| The no-polling rule makes the launcher feel stale in practice | Watermark and age are always visible, and explicit refresh is one keystroke; if this proves insufficient, the correct fix is a push notice mechanism under CD-0006 R3, never a poll |
| Knowledge sections turn the work screen into a document reader | Sections are bounded, lazily read, and scoped to the entity on screen; full reading happens in the session |
| Query results accrete columns and ordering rules until they become a fourth screen | Results are a view over the contract the screen already owns, with that contract's ordering; a distinct result schema would reopen §3 |
| Incomplete knowledge coverage is mistaken for "this entity has no knowledge" | `unavailable` carries a stable typed reason and bounded omissions, distinct from authoritative-empty |
| Knowledge resolution grows beyond a bounded section | The resolver remains one scoped read with explicit coverage; a broader browse surface reopens C18 |
| Latency degrades as screens gain content | The per-screen bound in §9 is an acceptance test, not an aspiration |

## 17. Falsifiers

This accepted contract should be reopened when:

- the operator cannot complete the §13 operator test without leaving the launcher;
- three screens prove to be the wrong cut, in either direction;
- the read-only boundary forces the operator to open a session for something that
  carries no consequence and no authority;
- knowledge-in-context proves insufficient and a genuine cross-entity browse job is
  named, which would reopen §3 rather than add a screen quietly;
- knowledge resolution requires a global browse surface rather than the accepted
  bounded owner-scoped section;
- ambient context proves insufficient and callers genuinely need to pass explicit
  scope, which would reopen `design-constraints.md` §13 rather than this document;
- the no-polling refresh model proves unusable in daily operation and a push notice
  mechanism is required; or
- the chosen rendering dependency fails the defined post-implementation assistive-
  technology validation, keyboard reachability, textual semantics, or latency clauses.

## 18. Evidence basis

- Product-first terminal launcher is the primary operator surface, and Priority 4
  requires bounded, fast, Product-scoped reads with reviewed staleness
  ([`priorities.md`](./priorities.md) §§Operating envelope, 1, 4–5).
- R1, R3, R4, R5, and R6 fix the launcher's role, factored lifecycle truth, the
  Product → Domain navigation path, active-work-first defaults, and Go ownership
  ([`clarifications.md`](./clarifications.md)).
- C14 fixes the row and explicitly defers interaction, keybindings, layout toolkit,
  and the detail screen ([`product-row-contract.md`](./product-row-contract.md)).
- C17 defines the accepted Product drill-down and defers the container to the launcher.
- Ambient Product context, typed degradation, no-polling impact propagation, bounded
  reads, single derivation, and non-destructive recovery are constraints §13, §2, §5,
  §14, and §19 ([`design-constraints.md`](./design-constraints.md)).
- Split state authority between an orchestrator and a projection is the recurring
  predecessor root cause, which is why §6 refuses to derive workflow position
  ([`advance-postmortem.md`](./advance-postmortem.md)).
- Initiative is an ordinary secondary work-item kind rather than an architecture
  container ([`decisions/CD-0041-architecture-bound-product-law.md`](./decisions/CD-0041-architecture-bound-product-law.md)).
- The read surface the launcher dispatches into is accepted TS3 under CD-0005, and the
  mutation surface it deliberately does not touch is TS4
  ([`agent-read-tool-contract.md`](./agent-read-tool-contract.md),
  [`agent-mutation-tool-contract.md`](./agent-mutation-tool-contract.md)).
- Go TUI dependency options, current as of 2026-08: `gdamore/tcell` v3 is released and
  breaking relative to v2, with `rivo/tview` not yet adopted
  (<https://github.com/gdamore/tcell>, <https://github.com/rivo/tview/issues/1145>);
  `charmbracelet/bubbletea` v2 publishes a component set covering tables, viewports,
  lists, help, and key maps (<https://github.com/charmbracelet/bubbletea>).

## Acceptance criteria

- Given a launcher session that enters S1, moves, scrolls, edits, pastes, and
  clears a filter, opens help, refreshes, selects, goes back, and quits
  When the session ends
  Then reads occurred only on entry and explicit refresh, and the session
  produced no durable effect.

- Given portfolio rows active, quiet, duplicate-a, and duplicate-b
  When the launcher projects the portfolio
  Then the rows resolve to the five C14 groups with a stable suffix
  distinguishing the duplicates.

- Given rows approval-required, active-problem, blocked, in-progress, and ready
  When the launcher orders focus
  Then approval-required wins and the remaining order is deterministic by
  priority, then time, then id.

- Given degraded, unreachable, and stale-blocked coverage states
  When the portfolio renders those sections
  Then unavailable counts are never presented as zero and focus absence
  carries its typed reason.

- Given a first run with no authority database
  When the operator opens the launcher
  Then the launcher renders its typed first-run state and creates no authority.

## Verification

The corpus `scenarios/launcher-portfolio.v1.json` encodes all five criteria as
declared cases bound to C14, C18, and CD-0014. No harness executes the
launcher corpus yet, so each criterion carries a typed exemption in the record
rather than a scenario binding.

- Criterion 1 is proved by `TestModelReadsOnlyOnEntrySubmitAndRefresh`
  (`internal/launcher/model_test.go`) and
  `TestLauncherSessionHasNoDurableEffects` (`cmd/concord/main_test.go`).
- Criterion 2 is proved by `TestProjectionIsDeterministicAndCarriesC14Meaning`
  (`internal/launcher/model_test.go`).
- Criterion 3 is proved by `TestProjectionIsDeterministicAndCarriesC14Meaning`
  (`internal/launcher/model_test.go`).
- Criterion 4 is proved by
  `TestReadDomainsMapsAbsentRegistryToTypedUnavailableSection`
  (`internal/launcher/storeport/port_test.go`).
- Criterion 5 is proved by
  `TestLauncherFirstRunRendersWithoutCreatingAuthority`
  (`cmd/concord/main_test.go`). Section 17 records the falsifiers for each
  guarantee.
