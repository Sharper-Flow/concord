# CD-0007: Concord Repository and Development Bootstrap

**Status:** Accepted; bootstrap execution authorized 2026-08-07
**Date:** 2026-08-07
**Decision owner:** Operator approval under the public bootstrap plan
**Scope:** Repository ownership, public migration, release/install boundary,
development governance, initial workflow/conformance floor, skills, privacy, and
supported platform.

## Context

CD-0002 through CD-0006 settle Concord's Product purpose, authority, workflow,
agent surface, governance, and migration policy. Runtime scaffolding remained
blocked only because Concord had no implementation repository/package boundary.

The contract was approved on 2026-08-06 with an explicit historical boundary:
contract approval alone did not authorize execution. On 2026-08-07, the operator
approved execution of the audited public bootstrap described here. The boundary is
preserved so the planning decision is not rewritten as if it had always authorized
execution.

## Decisions

### D1. Repository identity and Product authority

- Public remote: `https://github.com/Sharper-Flow/concord`.
- Go module: `github.com/sharper-flow/concord`.
- Public instructions use a generic clone destination; no personal checkout
  convention is part of the repository contract.
- Default branch: `main`.
- Concord is a standalone greenfield Product, not a predecessor subproject.
- Current constitutional specs, decisions, research, workflow contracts, scenarios,
  and runbooks are authoritative in the public Concord repository after the
  `constitutional-bootstrap` tag.
- No private history, private dependency inventory, archive bundle, or synchronized
  shadow copy is part of the public authority.

### D2. Public migration boundary

The public repository begins from an **audited current constitutional snapshot**,
not filtered private source history.

- Audit every current file for secrets, machine paths, private Product detail,
  private-only repositories, and non-public predecessor history.
- Publish current governing files in one constitutional starting commit.
- Add `docs/provenance.md` recording private design origin, preserved decision IDs/
  dates, and the public-authority start tag.
- Keep private source history non-public and non-authoritative.
- Replace private/local-source citations with public evidence or explicitly label
  unavailable private precedent.
- Public shipped scenarios are invented synthetic cases. Private-derived cases are
  excluded rather than shipped or uploaded.

**Visibility audit note:** Advance is the public predecessor source whose issue and
pull-request citations may remain as public references. Private product examples,
private history, and local-only precedent are excluded from this snapshot.

### D3. Product version, distribution, and installation

- One Concord semantic version covers core, launcher, adapter, contracts, workflows,
  and any shipped skills.
- One release may contain several matched artifacts.
- Distribution uses GitHub Releases plus a Concord-owned idempotent installer.
- Source checkout is development-only.
- Auto-release activates only after an installable artifact exists.
- Conventional commits determine subsequent version bumps.
- Releases include Linux amd64 artifacts, matched adapter/assets, checksums,
  changelog, and migration guidance.

### D4. Public governance

- Repository is public from creation.
- MIT license.
- Maintainer-led; issues and pull requests welcome.
- Consequential Product/workflow/authority changes are proposal-first.
- Operator retains final legislative and release authority.
- Required PR checks after bootstrap; governing contracts require maintainer review.
- Bug and Product-proposal templates, private vulnerability reporting,
  `CONTRIBUTING.md`, `SECURITY.md`, and Code of Conduct.
- No CLA/DCO, Wiki, Discussions, or GitHub Projects initially.

### D5. Platform, telemetry, and pre-readiness use

- Linux amd64 is the only supported v1 platform. Other platforms require concrete
  demand and complete native acceptance evidence.
- Telemetry/diagnostics are local-only. Nothing leaves the machine automatically;
  bounded diagnostic export requires explicit operator review/action.
- No prompts, work IDs, paths, spec/evidence contents, or secrets in telemetry.
- Before replacement readiness, Concord uses invented synthetic conformance cases
  only. It does not observe or own live Concord development.
- After full readiness, the Concord Product may migrate under CD-0006's normal
  Product-at-a-time fix-forward policy.

### D6. Replacement-ready workflow and conformance floor

Required workflow catalog:

1. implementation change;
2. break-fix/RCA;
3. research/investigation;
4. architecture spike;
5. ops runbook;
6. static-analysis family; and
7. generic one-off.

Database/configuration/infrastructure map to implementation or ops until a distinct
recurring progression/evidence/recovery shape proves another type necessary.

Replacement readiness requires:

- root-policy scenario families for spec mandates, impact propagation, workflow
  succession, proportional rigor, human legislation, authority-by-fact-type,
  Product migration, operator boundary, launcher boundary, and release evidence;
- per-workflow normal completion, missing evidence/approval rejection,
  interruption/resume, new-conflict routing, applicable successor linking, and
  durable-knowledge authority.

### D7. Skill/methodology boundary

- Default to workflow embedding or structural validation.
- Ship a skill only when methodology is reusable across workflows, prose-heavy, and
  materially better loaded on demand.
- Skill-shaped examples may include research/clarification, audit/review,
  architecture comparison, RCA technique, or skill-authoring methodology.
- Lifecycle, approval, spec mandates, impact blocking, evidence sufficiency,
  completion, schemas, and validators are never skills.
- Canonical skill form is a host-neutral OpenCode-compatible `SKILL.md`.
- Shipped skills use `concord-*` namespace and share the Concord Product version.
- Installed assets live under
  `${XDG_DATA_HOME:-~/.local/share}/concord/<version>/skills/`.
- OpenCode v1 discovers that versioned directory through one explicit
  `skills.paths` entry. This is schema/source-supported but prose-undocumented, so
  every supported OpenCode upgrade must run a compatibility check.
- Installer owns add/update/remove; no Product-repo copies, user-skill overwrite,
  plugin injection, new skill tool, or always-on methodology prompt.
- OpenCode restart is required after the registered version path changes.

### D8. Initial repository shape

```text
concord/
├── cmd/
├── internal/
├── adapter/opencode/
├── contracts/
├── workflows/
├── skills/
├── scenarios/
├── docs/
├── scripts/
└── .github/
```

No speculative daemon, service split, web app, plugin system, public Go package
surface, or package proliferation.

### D9. CI/release baseline

PR CI eventually covers Go formatting/lint/test/race, adapter typecheck/test,
contract-generation drift, documentation links, conformance scenarios, and Linux
amd64 artifact verification. Actions/toolchains are pinned; permissions are
least-privilege; dependency updates arrive through reviewed PRs. Release artifacts
must exclude secrets/private fixtures and include checksums/provenance.

## Bootstrap sequence (authorized 2026-08-07)

1. Prepare and audit the public candidate locally.
2. Review every file and outbound link.
3. Create `Sharper-Flow/concord` publicly.
4. Push one constitutional bootstrap commit.
5. Tag that commit `constitutional-bootstrap`; public Concord authority starts there.
6. Enable branch protections and CI.
7. Verify provenance and public authority links.
8. Begin runtime work through ordinary PRs.
9. First runtime slice is the accepted storage-spine conformance slice—not broad
   Product scaffolding.

## Historical boundary and current authorization

The 2026-08-06 contract approval did not itself authorize a checkout, public
repository creation, or migration. The operator's 2026-08-07 execution approval
authorizes the audited bootstrap sequence above only. It does not authorize runtime
implementation beyond the accepted readiness and development-authority rules, nor
does it authorize changes to unrelated operator configuration.
