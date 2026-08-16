# R8: Mid-execution discovery — silent-drop survey and mechanism

**Status:** Survey complete; mechanism selected by operator 2026-08-15 (root-cause
direction: avoid gates, fix the recording channel). Accepted as CD-0030.
**Motivating issue:** #99

## The survey

### What exists and fires today

| Piece | State | Fires on the scenario |
|---|---|---|
| CD-0012 D6 substitution guard | shipped | no — nothing was substituted |
| Premise confirmation, evidence binding | shipped | passes — the approved scope is genuinely delivered |
| Declared-impact / CD-0006 R3 | shipped | no — no edge declared |
| `link_successor`, capture + `raised_from` (CD-0018) | shipped | optional acts, never invoked |

Every obligation is satisfied; the item closes clean; the systemic finding is gone.

### Signal soundness: the issue's computable-divergence hypothesis

The hypothesis: touched-refs (checkpoints) versus declared scope (contracts) is
computable at completion, so a discovery gap may be derivable rather than
volunteered. **Finding: unsound as an owner of correctness.**

- `touched_refs` are **self-reported** by the agent in `checkpoint_execution`
  payloads (free-form bounded ids; `workflow_context_checkpoints.touched_refs`,
  ≤64). The five-siblings finding produces divergence only if the agent both
  read the siblings *and* honestly reported them — two conditions that can
  silently fail together, because self-report is the failing channel (P33: a
  heuristic may not own a correctness gate).
- Declared scope is machine-comparable only as `workflow_candidate_sets`
  (typed include-refs); premise and `spec_mandate` are prose.

**What is sound:** refusing a *contradiction between the agent's own durable
statements* — a completion declaring "nothing discovered" against a checkpoint
that touched undeclared refs with no successor link. Coherence-checking recorded
evidence is the same class as `resolved ≠ readback`. This powered the
gate proposal the operator declined.

## Root cause

The issue states it: the agent-native envelope removes the channel a human has.
A human who notices five broken services *says something* — mid-thought, at near
zero cost, without first deciding the finding deserves to be tracked work. An
agent's recording acts — capture, `link_successor`, `raised_from` — are all
work-management decisions: each requires deciding **work-hood before recording
the observation**. The silent drop is not dishonesty; it is friction plus the
absence of a natural act for "I noticed something."

## Selected mechanism (operator direction: avoid gates, fix the channel)

**Observations: a lightweight, durable, mid-life recording act on the work
item.** `work.observation_recorded` persists a bounded statement (≤512 chars,
optional refs, optional tags) any time the item lives, through one cheap
mutation. No work-hood decision, no gate, no completion interaction, no
substitution surface (observations are explicitly non-authoritative — they
satisfy nothing).

**Read-time visibility, not enforcement.** The continuity snapshot carries the
un-promoted observations, so the next session — and the operator — resume into
them. The drop becomes *checkable after the fact* (an item closed with
observations nobody promoted) rather than *prevented by assertion*. This is the
same trust position as a human colleague: possessing a one-call way to say
something and choosing not to is a different act from having no way to say it.

**Promotion unchanged.** Turning an observation into work remains the existing
deliberate path: capture with a `raised_from` provenance edge to the observing
work (CD-0018), or any relation the finding warrants. The observation is the
substrate; promotion is a later decision by anyone — operator, next session, or
the discovering agent once it decides the finding matters.

## Rejected alternatives

- **Completion declaration gate** (declaration + coherence refusal): sound under
  P33 and cheap, but rejected by operator direction — it treats the symptom
  (missing assertion) rather than the cause (missing cheap act), and puts the
  obligation at the boundary where the agent is closing, not where the
  discovery happens.
- **Divergence-only gate**: unsound — self-report is the failing channel; the
  gate can be silently skipped by the same omission it guards against.
- **Non-blocking omission surfacing alone**: weaker variant of visibility
  without fixing the channel; absence-of-assertion remains today's failure.
- **No mechanism / prompt discipline**: argued against by the motivating
  scenario; the channel, once cheap, makes discipline enforceable by review
  against the durable record.

## Provenance note

R1–R7 precedent (durable design evidence feeding accepted decisions; CD-0009 D8
as amended by #143's decision record) applies: this document is spike evidence,
not active research-pack content.
