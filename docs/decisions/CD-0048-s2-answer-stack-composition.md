# CD-0048: S2 composes the answers the store already materialized

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** `docs/terminal-launcher-contract.md` §5 Tab semantics and S2
  composition; issue #228
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #228 comment](https://github.com/Sharper-Flow/concord/issues/228#issuecomment-5361273350)
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

### D1. S2 renders as a panel stack in §3's job order

One vertical stack, one panel per §3 clause, top to bottom: governing
Domain with its overlap state, blocked and blockers, next work. The ambient
Product line above the stack stays as §8 requires. Domain hierarchy browse and
the bounded knowledge section render inside the expanded panels.

### D2. Collapsed panels carry the answer and its reason

When focus leaves a panel it collapses to one stable line showing the
store-materialized value and its reason — the attention tier for the Product,
the overlap pair and resolution state for the Domain, blocker identities with
authority for the blocked panel. A Domain with no unresolved overlap keeps a
stable line stating that, so evaluated-clean stays distinguishable from
unevaluated and redraw remains idempotent per §8.

### D3. Ordering is unchanged and unchangeable here

Panel order is §3's job-order sentence. Panel values order by the stored rank
and existing deterministic store tiers. No launcher-side score, weighting, or
recency inference exists in this composition; §12.6 applies to summary lines
exactly as it applies to rows.

### D4. §5's Tab row is refined, not replaced

Tab cycles panel focus within S2's stack instead of swapping full-screen
sections. The keymap table's Tab row changes its Action text to match; the
S3 knowledge section keeps its existing Tab behaviour. No other §5 key
changes, and §11's widget floor is untouched.

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

- `docs/terminal-launcher-contract.md` §5's Tab row matches D4; no other
  section changes.
- `python3 scripts/check-doc-links.py`, `python3 scripts/check-public-content.py`,
  and `python3 scripts/check-knowledge-index.py` pass with this record indexed
  exactly once.
- Implementation acceptance tests assert: panel order fixed, summary lines
  carry store-materialized values only, no-overlap Domain renders a stable
  line, redraw over unchanged state is byte-identical.
