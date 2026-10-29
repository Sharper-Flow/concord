# Concord first-usable floor

The first-usable floor is defined in two places: the *First-usable floor* section
below (the six usability-floor conditions) and
[`rollout-plan.md`](./rollout-plan.md) under *Release-evidence bar* (the
release, install, privacy, and Linux amd64 bar).

## First-usable floor

The alpha maturity rung requires the floor to stay met
([CD-0091 D3](./decisions/CD-0091-maturity-promotion-ladder.md#d3-the-maturity-rungs-and-their-evidence-bars)).
Concord's current direction is in [`goals.md`](./goals.md) and
[`priorities.md`](./priorities.md).

1. The operator can see, plan, and act across the full Product scope through Product → Domain context. Per CD-0021, "across the full Product scope" means every Product is reachable, not that a result set spans Products. The host starts operator sessions, and agents enter managed work through `concord_work_start` under [CD-0219 D4](./decisions/CD-0219-the-terminal-launcher-tui-is-retired.md#d4-kept-clauses-keep-their-surviving-owners). Planning stays in Linear through the Linear MCP server under [CD-0213 D1](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d1-linear-is-the-only-planning-authority); managed work creation keeps one write authority.
2. Agents can read, write, and execute within that Product and Domain context through tools.
3. Every supported workflow family (implementation, break-fix, research) has a defined workflow type and completion evidence.
4. Required authority/freshness is explicit and CD-0006 R3's accepted cross-workflow impact policy is enforced.
5. Concord-owned data and workflow boundaries preserve data safety (bounded writer admission, no hand-repair, safe evolution).
6. The Advance surfaces that Concord consumes are stable enough to serve as evidence, and Concord can replace them without losing operational coverage.

## What this is not

It is a definition, not a plan. It does not sequence work, assign effort, set
dates, or authorize implementation. A replacement-readiness claim requires every
condition met, judged against each condition's source section.

It does not change the development-authority model in
[`managed-development-authority.md`](./managed-development-authority.md): Linear
through the Linear MCP server owns planning, and pull requests with required
checks own review and merge evidence.
