# CD-0048: S2 composes the answers the store already materialized

- **Status:** Accepted (amended 2026-09-26)
- **Date:** 2026-08-20
- **Scope:** `docs/terminal-launcher-contract.md` §5 Tab semantics and S2
  composition; issue #228
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #228 comment](https://github.com/Sharper-Flow/concord/issues/228#issuecomment-5361273350).
  The operator approved the single-surface amendment on 2026-09-26
  ([Concord (CON) issue 472](https://linear.app/sharper-flow/issue/CON-472/tui-layout-cleanup-survey-driven-per-view-redesign));
  D1, D2, and D4 below carry it.
- **Related:** CD-0014, CD-0016, CD-0041,
  [`terminal-launcher-contract.md`](../terminal-launcher-contract.md) §3, §5,
  §8, §11, §12, §13
- **Preserves:** the closed three-screen set; the §11 widget floor; §12
  anti-requirements 6 and 8; the read-only action surface; the §8 rendering
  constraints
- **Supersedes:** nothing; refines §5's Tab row

## Context

§13 states the operator test: from a cold start, identify the Product needing
attention, enter it, name the governing Domain and unresolved architecture
overlap, name what is blocked and what blocks it, and resume the next work item
— without leaving the launcher and without restating any path.

Every clause is materialized before the launcher renders:

| §13 clause | Materialized by |
|---|---|
| Product needing attention | five-tier `attentionKind` (approval_required → active_problem → blocked → in_progress → ready), `internal/store/product_row.go` |
| Governing Domain + unresolved overlap | `DomainOverlapPair` with `ResolutionState == "absent"`, `internal/store/domain_reads.go` |
| What is blocked, and what blocks it | `Blocked` flag plus `Blockers[]` with authority, `internal/store/launcher_query.go` |
| Resume the work item | `product_id` + optional `work_id`; the whole session input, `cmd/concord/session.go` |

§3 assigns S2 a four-part job in a fixed order: what law governs, what
overlaps, what is blocked, what is next. The current S2 renders those four as
full-screen Tab-cycled sections, so the operator walks each section and holds
the assembled answer in working memory. The navigation path is discarded at
handoff — the CD-0016 packet carries only identity and re-derives everything
else — so the assembly the operator performs is not an input to anything
durable. It is presentation work with no downstream consumer.

The assembly is also work the launcher is prohibited from making easier by
computation: §12.6 forbids model-assigned ordering. The only lawful way to
shorten the operator's path is to compose the values the store already
materialized, in the order §3 already states.

## Decision

### D1. S2's answers stay store-materialized; the panel stack is superseded

Amended 2026-09-26: the launcher no longer renders S2 as a panel stack. Per
CD-0108 D2 as amended, each Product screen renders one work list; the
governing Domain and its overlap state surface as a single line only when an
unresolved overlap exists; blocked and next state carry through the work
list's readiness markers, blocking ticket references, and `updated_at`-descending
ordering. The Context table above still holds: every §13 answer remains
materialized by the store before the launcher renders, and the launcher adds
no computation over it.

### D2. Quiet means healthy

Amended 2026-09-26: a Domain with no unresolved overlap renders nothing, per
CD-0108 D2 as amended. Evaluated-clean stays distinguishable from unevaluated
at the store boundary: the abnormal line names the pair and its resolution
state, and redraw over unchanged state stays byte-identical.

### D3. Ordering stays store-owned

Amended 2026-09-26: the work list orders by stored `updated_at` descending
with a deterministic tiebreak. Recency is a stored column, not a launcher
inference; no launcher-side score, weighting, or model-assigned ordering
exists, and §12.6 applies unchanged.

### D4. §5's Tab row realigns with the successor contract

Amended 2026-09-26: Tab no longer cycles panel focus, because the panel stack
is gone. The §5 key table realigns when the successor launcher contract
registers; that realignment is the tracked follow-up and changes nothing here.

### D5. The action surface does not move

The stack is display plus navigation. Naming an unresolved overlap is not
resolving it; launch remains the only key with an external effect, and the
launcher remains read-only by construction (§2, §12.1).

## Consequences

- §5's Tab row Action text is the only contract edit; it lands with this
  record.
- S2's renderer composition changes inside the Bubble Tea adapter; the
  framework-independent model gains panel-focus state. No store, schema,
  contract, digest, or generated-file change.
- The implementation issue opens only after this record is accepted, and
  depends on #231 (navigation-stack restore) landing first.
- The `relations.kind = 'implements'` no-consumer finding stays outside this
  record and tracks separately.

## Rejected alternatives

**A four-screen drill-down (Product → Domain → Work list → Detail).** Rejected:
it amends §3's closed set for no gain, and work-to-Domain binding is a graph
(`home_domain_id`, `affected_domain_ids`, `domain_modifies`), not containment —
a drill-down forces an arbitrary home-versus-affected choice the data does not
make.

**An accordion or tree widget.** Rejected: the Bubble Tea v2 ecosystem ships
none (the bubbles tree proposal remains an unmerged pull request), and
collapse here is a summary line, not a fold — no widget would own any state
worth its dependency.

**A dashboard surface.** Rejected: §12.8 prohibits dashboard drift, and §2
already narrows the launcher to status plus resume.

## Verification

- `docs/terminal-launcher-contract.md` §5's Tab row realigns with the successor
  launcher contract registration; no other section changes with this amendment.
- `python3 scripts/check-doc-links.py`, `python3 scripts/check-public-content.py`,
  and `python3 scripts/check-knowledge-index.py` pass with this record indexed
  exactly once.
- Store materialization of the §13 answers is proved by
  `TestS2DomainSectionReadsLawRelationsWorkAndOverlapFromTheStore`,
  `TestS2ArchitectureRelationsAreAuthoritativeEmptyNotUnavailable`, and
  `TestS2DomainSectionBoundedOverlapKeepsRegistryRows`
  (`internal/launcher/storeport`).
- Rendering after the amendment is proved by
  `TestDomainContextRendersOnlyWhenAbnormal`,
  `TestWorkListProjectionCarriesMarkerKeyTitleBlockersAndLive`,
  `TestWorkListProjectionIsRecencyOrdered`, and `TestWorkListRedrawIsByteIdentical`
  (`internal/launcher`, `internal/launcher/render/bubbletea`): abnormal-only
  Domain lines, store-materialized row values, recency ordering, and
  byte-identical redraw over unchanged state.
