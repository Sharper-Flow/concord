---
{
  "id": "CD-0255",
  "title": "Per-Project repository mode",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0200"
    }
  ],
  "product_wide_rationale": "Repository mode binds Project storage, the operator command surface and installation, and agent reads and conduct; no single child Domain owns all affected surfaces."
}
---
# CD-0255: Per-Project repository mode

- **Status:** Accepted
- **Date:** 2026-11-06
- **Scope:** Project state in the authority store, the operator CLI, the
  installer's project link, agent reads of Project facts, and agent conduct in
  repositories.
- **Related:** CD-0200 D1, CD-0243, CON-1742

## Context

Concord could not tell whether a repository is driven by Concord. Nothing
stopped a local Concord agent in someone else's repository from staging,
committing or pushing Concord's files — `.concord/`, which the Product authors,
and `.opencode/opencode.json`, which `concord-installer.py link --project`
writes. Asked where the switch lives, the operator decided on 2026-11-05:
"Explicit setting, off by default, but we would want it on for our concord
projects on this host."

## Decision

### D1. Each Project carries an explicit repository mode

Each Project carries `repository_mode`, `on` or `off`. It is current state in
the authority store, advanced only by `project.repository_mode_set` effects,
and only the operator sets it through `concord project repository-mode-update`.
A new Project starts off. The setting never changes by itself; no git state,
locator or observation derives it. Agents and hosts read it through the product
view resolve read and `concord project resolve`; both name the Project's mode.

### D2. On: Concord's files are ordinary Product files

In an on-mode Project, `.concord/` and Concord's agent files are committed like
any other Product file. On does not mean only Concord agents may work on the
repository; nothing restricts people or agents who do not use Concord.

### D3. Off: Concord's files stay out of git

In an off-mode Project, Concord agents may work locally, and they never stage,
commit or push Concord's files. The installer's `link --project` keeps its
written file and `.concord/` out of git through the clone's local exclude
(`.git/info/exclude`), which is never committed; link and unlink add and remove
those entries idempotently. A repository the resolver cannot resolve is treated
as off. Shipped conduct guidance names the rule for agents; guidance plus the
local exclude carry it, and Concord adds no enforced commit or push gate
(CON-1742 owner decision, 2026-11-06).

### D4. Off-mode Projects have no Product law by default

An off-mode Project has no Product law. Its agents follow Concord's shipped
guidance and the repository's own documentation. The operator may designate a
separate knowledge-home repository for it under CD-0200 D1; only that
designation gives it Product law, and it names a separate repository, never the
off-mode Project itself.

## Verification

- Store tests pin the default, both transitions, the version check, and that
  unrelated Project events leave the mode unchanged; the mode advances only
  through `project.repository_mode_set` effects, and the command that appends
  them exists only on the operator CLI surface. Restored-copy upgrade evidence
  covers migration 191, and `scripts/check-migration-compatibility.py` binds its
  advance declaration to `Breaking: true`.
- An installer test proves a linked off-mode clone shows no Concord file —
  neither the installer's project config nor `.concord/` — in `git status`, and
  that an on-mode link adds no exclude entry.
- Agent contract tests pin `repository_mode` in the resolve read payload.
- Independent review checks D1-D4 against the delivered surfaces.
