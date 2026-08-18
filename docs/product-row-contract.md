# Concord Product-Row Glance Contract (C14)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** C14; default terminal-launcher Product row and compatible optional
> admin-panel projection.
> **Amended by:** CD-0041 replaces component navigation with Product → Domain;
> the five Product-row field groups remain unchanged.
> **Binding inputs:** Product-first operating envelope, Product/Domain navigation,
> active-work visibility, PM1 Q1/Q2/Q4/Q5, PM4/PM5 identity semantics, and accepted
> TS7 authority/freshness envelope.
> **Does not decide:** terminal interaction/keybindings/layout toolkit, Product detail
> screen, Domain hierarchy/architecture graph, C15 managed-resource model, optional live-signal display,
> workflow registration, or web/admin layout.

## 1. Decision

The default Product row contains exactly five field groups:

1. **Identity** — stable Product ID plus display name;
2. **Declared stage** — Product-default maturity and audience commitment;
3. **Reliance state** — authority/freshness and any execution-blocking stale state;
4. **Action counts** — in-progress, blocked, ready, active-problem, and
   approval-required counts; and
5. **Focus item** — one deterministic highest-attention/current/next work summary,
   or an explicit reason no focus item exists.

The row is an orientation/selection projection, not a Product dashboard. The launcher
supports narrow open/start/resume/launch routing only. Selecting a row opens the
Product/Domain/workflow detail where architecture overlap, approvals, conflicts,
editing, history, law, operations, and resources belong (CD-0006 D6; CD-0041).

## 2. Canonical row object

```text
product_id
display_name
stage {
  maturity             # prototype|alpha|beta|production|deprecated
  audience_commitment  # operator_only|limited|public
}
reliance {
  authority            # authoritative|degraded|unreachable
  observed_at
  age                   # milliseconds
  stale
  blocks_execution
  reason?               # source_stale|source_lag|authority_unreachable|invariant_violation
  omissions[]           # bounded typed omissions when degraded/unavailable
}
action_counts {
  state                 # known|unavailable
  values? {
    in_progress
    blocked             # PM4 derived, never stored lifecycle
    ready               # needed with no unresolved blockers
    active_problems     # problem-kind work in needed|in_progress
    approval_required   # offered action currently requires human authority
  }
  unavailable? { reason, omissions[] }
}
focus {
  work_id
  title
  work_kind
  lifecycle
  attention_kind
  priority
  workflow_step_label?
  project_count
  stage_context {
    kind                # product_default|single_focus_override|mixed
    focus_override?     # one declared stage only for single_focus_override
  }
} | null
focus_absent_reason?    # stale_block|unreachable|no_actionable_work|authoritative_empty
```

The stable ID keys selection and navigation but need not consume visible columns
unless names collide or the operator asks for detail. When display names collide on
one page, every colliding row shows a fixed suffix derived from the immutable Product
ID; disambiguation is required, not optional. Counts are derived from one canonical
PM1 snapshot and never from per-Project copies.

The five counts intentionally overlap: blocked work retains its lifecycle, problems
are a work kind, and approval may apply to either. They are never summed into a
"total." If required source coverage is incomplete, the whole count group is
`unavailable`; degraded/unknown data cannot render as zero.

## 3. Focus selection

Choose at most one canonical work item, deduplicated before ranking. First non-empty
tier wins:

1. work awaiting required human approval;
2. active problem-kind work;
3. blocked work;
4. in-progress work;
5. ready work.

Within a tier, order by explicit priority, relevant lifecycle time, then stable work
ID, matching PM1. Stage does not silently rewrite business priority. Cross-Project
work appears once; `project_count` signals its breadth without listing repositories.

If reliance state blocks execution, the row may still identify the stale focus for
orientation, but direct action stays blocked and `focus_absent_reason=stale_block`
when no safe focus can be established.

## 4. Rendering rules

The field contract is shared; terminal and optional admin panel may render it
differently.

- Product name and stage are always visible.
- `unreachable`, blocking stale state, and required approval are visually dominant.
- Zero counts may be suppressed visually, but remain present in the projection.
- Focus title is truncated before identity, reliance, stage, or count meaning is
  removed; full title appears after selection.
- `authoritative` freshness may render quietly. `degraded|unreachable|stale` never
  hides behind color alone and includes a text/icon marker.
- No horizontal scrolling is required for the default row. Narrow rendering may
  move focus to a second line but cannot add new fields.
- Accessibility does not depend on color; badges have stable textual symbols/labels.

## 5. Explicit exclusions

The default row does **not** include:

- terminal/completed/cancelled/superseded counts or recent history;
- repository paths/remotes, Project names/list, worktree/branch, or local process
  state;
- C15 resource inventory, SaaS/infrastructure membership, secrets, or provider data;
- optional live-signal/health/uptime/deployment status;
- last-activity timestamp, commit age, velocity, percent complete, estimates, owners,
  assignments, sprint, or board state;
- spec/runbook/document titles;
- raw blocker graph, evidence details, gate checklist, or workflow artifacts; or
- action buttons/commands beyond selecting the Product.

These either belong after drill-down, lack accepted authority, duplicate another
count, encourage activity-as-value inference, or exceed the Product row's glance job.

## 6. Why these fields

- **Identity** answers which Product will become ambient context.
- **Stage** exposes the declared default evidence context and keeps maturity separate
  from user-declared audience responsibility. Focus reports whether it uses the Product default,
  one distinct override, or mixed declarations; it never invents an ordering across
  multiple maturity/audience combinations.
- **Reliance** tells the operator whether the row is safe to act on; stale/degraded
  data cannot look authoritative.
- **Action counts** cover Priority 4/5's present coordination questions—what is
  active, blocked, ready, problematic, or waiting on the human—without backlog or
  historical clutter.
- **Focus** answers the next glance-level question without turning every bucket into
  a list. Deterministic ranking makes repeated renders stable for unchanged state.

## 7. Candidate comparison

| Candidate | Decision |
|---|---|
| Name only | Rejected: cannot expose portfolio blindness, reliance risk, or what needs attention. |
| Name + last activity + Project/path | Rejected: activity is not value/priority; path is locator, not Product identity; current ZLauncher evidence found per-project detail low-signal. |
| Name + every lifecycle/resource/history count | Rejected: dashboard density, terminal clutter, terminal history in default view; accepted C15 resources remain drill-down inventory. |
| Five groups selected above | **Selected:** covers Product choice, risk, active coordination, and next focus with one bounded projection. |

## 8. Read-path and performance

One bounded Product-row query/projection returns all five groups for a portfolio page.
It must not issue per-Product Q2/Q4/Q5 fan-out. The Go read path derives rows from the
same typed projections and authority watermark as PM1.

- Page default 20, maximum 100 Products.
- One focus item maximum per row.
- Rows have one shared source/version watermark plus per-row reliance when sources
  differ.
- PM1 implementation target applies: P99 ≤100 ms locally at 10× measured dataset.
- An unavailable required authority returns typed unreachable/degraded state, never
  cached-looking current counts.

## 9. Prototype acceptance

Test at minimum:

- authoritative active Product with all nonzero count kinds;
- quiet Product with authoritative empty work;
- Product with approval/problem/blocked/in-progress/ready competing for focus;
- cross-Project focus deduplication;
- mixed-stage Product covering default, one focus override, and multiple distinct
  declarations without ranking them;
- stale-blocked, degraded, and unreachable rows;
- duplicate display names requiring ID disambiguation;
- narrow and wide terminals without hidden meaning or horizontal scroll;
- 20- and 100-Product pages under read latency/output bounds; and
- screen-reader/no-color textual interpretation.

Operator test: identify the Product requiring attention, explain why, and select the
next Product in one glance without opening rows. Failure to do so reopens fields or
focus priority; it does not authorize a dashboard dump.

## 10. Evidence basis

- Primary operator surface is Product-first terminal; operator must see ready,
  blocked, and next work (`priorities.md` §§Operating envelope, 4–5).
- Default Product/Domain view shows active gates/problems first and keeps terminal
  history behind drill-down (`product-data-model.md` §§6–7).
- PM1 Q2 provides unique lifecycle/derived counts and bounded previews; Q4/Q5 own
  blocker/ready semantics and deterministic priority (`product-memory-query-contract.md`).
- Current ZLauncher uses fixed-width concise title/progress rows, and its source notes
  that a per-project detail preview was removed as low-signal
  (`dev/zellij-project-launcher/zellij-project-launcher`).

## 11. Falsifiers

Reopen C14 when:

- operator prototype cannot reliably identify attention/next Product;
- five counts are not distinguishable at glance or duplicate the same signal;
- focus ranking repeatedly hides the item the operator must handle first;
- declared Product stage misleads for mixed-stage active work despite focus override;
- row width forces hidden reliance/action meaning;
- one-query projection cannot meet bounded latency at portfolio size; or
- accepted C15/live-signal work proves one additional row indicator prevents a
  recurring unsafe action without becoming dashboard clutter.

Any added field requires a named operator glance job and prototype evidence. Being
available in storage is not evidence it belongs on the row.
