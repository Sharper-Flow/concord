# CD-0157: The registry gains an advisory utility

- **Status:** Accepted
- **Date:** 2026-09-19
- **Amended:** 2026-09-20. D4 reclassified the model self-report from a
  control to a disclosure for post-hoc audit, and the Consequences bullet now
  claims audit, not detection.
- **Scope:** The closed utility registry; the generated advisory body and tool
  allowance; the installer agent file list; [Concord (CON) issue
  315](https://linear.app/sharper-flow/issue/CON-315/add-a-concord-advisor-utility-for-coordinator-second-opinions)
- **Approval:** The operator approved this bounded registry change.
- **Related:** CD-0149, CD-0102, CD-0054, CD-0017, and CD-0081
- **Preserves:** The worker authority boundary; the utility distinction from
  lane evidence; fail-closed tool and command permissions
- **Supersedes:** Nothing

## Context

A coordinator reaches decisions alone. The registry holds five worker lanes and
three utilities. Research, exploration, and lookup return findings. Review
judges a change that already exists. No surface returns a reasoned opinion on a
decision the coordinator has not yet made.

Measured results constrain what such a surface may look like. A critic that sees
a proposed answer anchors to it. Chain-of-thought, reflection, and direct
instructions to ignore the anchor do not remove that shift. A strict critic
persona adds a downward bias that agreement between critics does not correct.
A request for detailed faults and fixes makes fault-finding worse. Opinion-only
critique degrades performance, while a critic that reaches tools corrects
reliably.

One further result bears on routing. Verification by the same model family
yields less than verification across families, and the bias resists detection.
Concord cannot act on that result. The host picks the serving model after the
adapter has admitted the call, and CD-0054 D3 keeps the registry model-neutral.

## Decision

### D1. The registry admits `advisor` v1

The utility has purpose `Give an independent reasoned opinion on a bounded
problem and cite the evidence for it.` It allows `bash`, `read`, `glob`, `grep`,
and `execute`. Its bash allowance is `git diff *`, `git log *`, `git show *`,
`git status *`, `git ls-files *`, and `git rev-parse *`. Its wall-time cap is
600 seconds. It has no packet, report schema, or evidence obligation.

The allowance matches `explore` because the same reasoning applies. An opinion
with no evidence channel is the configuration that fails. The adviser must cite
a path, a line range, or a command for each finding.

`webfetch` is absent. External questions belong to `lookup`, and the two
utilities keep separate jobs. CD-0149 D1 governs the limit of that exclusion.
`execute` reaches the host tool interpreter, and the host owns what that
interpreter exposes. This record therefore guarantees that the adviser cannot
change a repository file or record Concord state. It does not guarantee that
every host tool is out of reach.

### D2. The adviser receives a problem, never a proposed answer

The caller passes the problem, its constraints, and the intent behind it. The
caller does not pass its own conclusion, its preferred option, or its ranking.

Two independently reached answers make disagreement informative. A critique of a
supplied answer makes agreement the default outcome. Ordering the two inside one
session does not help, because the proposal contaminates the context once it
arrives.

### D3. The body is collaborative and may report no concerns

The generated body asks the adviser to solve the stated problem. It assigns no
critic persona, no severity scale, and no verdict. A result of no concerns is
explicitly permitted and is not a failure.

### D4. The adviser states the model that served it

The generated body requires the adviser to name its serving model in its
output. Amended 2026-09-20: the self-report is a disclosure for post-hoc
audit, not a control.

Concord cannot enforce that the adviser and the caller differ in model family.
The adapter admits the call at `tool.execute.before`, and the host routing layer
picks a model after that point.

No mechanism instructs a caller to compare the disclosed model against its own
serving model, or to discount on a match. The coordinator definitions, the
conduct corpus, and the adapter carry no such rule. A disclosure with no
comparing consumer is not detection.

The routing layer computes the family collision mechanically. When no disjoint
rung exists, it serves the same-family model and cites this record's
self-report as the reason that is safe. Its own schema calls the bias hard to
detect downstream.

An unsatisfiable disjointness constraint therefore yields a contaminated
consult that no mechanism detects in-band. A human auditing a transcript after
the fact can compare the disclosure. The same-turn agent consumer cannot be
relied on to: in the first live consult the coordinator performed the
comparison by hand and reached the wrong answer.

Observed evidence, routing log of 2026-09-20: two live consults, and each
self-report matched the model the log shows serving it. Neither consult ran
under a rollover. Disclosure accuracy under rollover is unverified.

### D5. Advisory output is not evidence

CD-0149 D4 applies without change. The adviser returns text to its parent. It
records no workflow state, starts no nested worker, and discharges no lane
report. It gates nothing. A coordinator that accepts an adviser's objection
carries that objection into the work record itself.

## Consequences

- The manifest digest moves, and all generated projections regenerate.
- The installer ships `concord-advisor.md` with the existing definitions.
- The adapter receives a typed utility entry without a new dispatch path.
- A lane cannot consult the adviser. The adapter refuses a utility called from a
  session with a managed parent.
- The value of a consult depends on host routing this registry cannot verify.
  An operator who points the adviser at the caller's own model family gets a
  weaker result, and D4 makes that case auditable after the fact, not detected
  in-band.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `python3 scripts/check-agent-contracts.py` validates the closed registry and
  the adapter projection.
- The generator tests prove the advisory tool projection and body template.
- The adapter session-scope tests prove that a session with a managed parent
  cannot call the adviser.
