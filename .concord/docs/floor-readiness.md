# Concord first-usable floor

The first-usable floor is defined in two places: the *First-usable floor* section
below (the five usability-floor conditions) and
[`rollout-plan.md`](./rollout-plan.md) under *Release-evidence bar* (the
release, install, privacy, and Linux amd64 bar).

## First-usable floor

The alpha maturity rung requires the floor to stay met
([CD-0091 D3](../law/decisions/CD-0091-maturity-promotion-ladder.md#d3-the-maturity-rungs-and-their-evidence-bars)).
Concord's current direction is in [`product-vision.md`](./product-vision.md) and
[`../law/constitution/priorities.md`](../law/constitution/priorities.md).

1. The operator can see, plan, and act across the full Product scope through Product → Domain context. Per CD-0021, "across the full Product scope" means every Product is reachable, not that a result set spans Products. The host starts operator sessions and owns worktree placement under CD-0088 D1. Concord derives managed context under [CD-0219 D4](../law/decisions/CD-0219-the-terminal-launcher-tui-is-retired.md#d4-kept-clauses-keep-their-surviving-owners). Planning stays in Linear through the Linear MCP server under [CD-0213 D1](../law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d1-linear-is-the-only-planning-authority); managed work creation keeps one write authority.
2. Agents can read, write, and execute within that Product and Domain context through tools.
3. Every workflow type the step-template catalog declares ([`contracts/workflow-step-templates.v1.json`](../../contracts/workflow-step-templates.v1.json)) has defined required steps and completion evidence.
4. Required authority/freshness is explicit and CD-0006 R3's accepted cross-workflow impact policy is enforced.
5. Concord-owned data and workflow boundaries preserve data safety ([concurrent-writer correctness](../law/decisions/CD-0045-writer-admission-invariant.md), no hand-repair, safe evolution).

Assess a candidate against each condition's source and the release-evidence bar,
using evidence specific to that candidate. Independent review examines the
evidence and any gaps; no condition is presumed met. This assessment creates no
manifest or second authority.

## What this is not

It is a definition, not a plan. It does not sequence work, assign effort, set
dates, authorize implementation, or promote a Product by itself. A first-usable
claim requires every condition met, judged against each condition's source section.

It does not change the development-authority model in
[`../law/specs/managed-development-authority.md`](../law/specs/managed-development-authority.md): Linear
through the Linear MCP server owns planning, and pull requests with required
checks own review and merge evidence.
