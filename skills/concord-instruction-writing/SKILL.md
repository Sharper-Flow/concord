---
name: concord-instruction-writing
description: Write, edit, or prune agent instructions. Use for agent and role profiles, shared instruction files, skills, AGENTS.md or CLAUDE.md, and agent-definition manifests, including source text that generators project into instructions.
---

# Instruction writing

This portable method guides wording and organization. It adds no authority,
approval, or workflow gate under [method admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0246-concord-ships-portable-engineering-methods.md).
For Product law, also load the shipped `concord-law-writing` skill for ownership,
meaning review, approval and publication.

## Find the instruction's owner

Read the current instruction and the surface that loads it. Identify its reader,
the behavior it must change, and the source that owns that behavior. Edit the
source and regenerate projections through their existing generator.

Give each meaning one canonical home. An agent profile routes role-specific work;
a shared instruction holds conduct used across roles; a skill carries an
occasional method. Amend the owner instead of adding another rule beside it.
Leave facts that one file or command already exposes with that source. Retain
rationale for a constraint the environment cannot explain.

## Make the pointer fire

A context pointer names material outside the current context and states when to
load it. Skill descriptions and profile routing sentences are both pointers.
Their wording decides whether the method reaches the agent.

Front-load the task and name each distinct trigger branch once. For instruction
writing, profiles, shared instructions, skills and definition manifests are
different branches; several synonyms for editing are not. Use the exact loadable
skill name. When a needed method is missed, sharpen its pointer before copying
the whole method into always-loaded text.

## Order the material

Separate ordered actions from reference consulted on demand. Keep what every
branch needs in the main path; put branch-specific detail behind an explicit
pointer. Keep definitions beside their conditions, exceptions and examples.
Disclose reference when it would otherwise bury the next action, not merely to
make the main file shorter.

Always-loaded prose spends attention in every session. A pointer spends less
context but makes the reader choose when to follow it. Keep that choice explicit
and test it; hiding required material behind a vague pointer is not pruning.

## Make completion decidable

State who does what, when, and what observable result ends the step. Demand the
actual coverage needed: accounting for every affected caller is stronger than
producing a change list. Replace a vague completion condition before adding
another reminder to be thorough.

If later steps demonstrably cause the reader to rush an earlier one, separate
them at a real handoff boundary. Another heading in the same context does not
hide the later work. Split only when the cut improves execution or invocation,
not because a document crosses a word count.

## Prune and verify

- Delete a sentence that changes no decision or behavior; trimming every word
  preserves the same unnecessary load. Remove duplicated meaning, stale history
  and exposition that does not guide the reader.
- State the target behavior positively. Keep a prohibition for a real boundary,
  paired with the permitted next action when one exists.
- Reuse the reader's vocabulary. A compact, familiar concept can replace repeated
  explanations; a coined label needs a definition and may cost more than it saves.
- Compare old and new actors, scope, triggers, outcomes, exceptions, evidence,
  timing and authority. Keep intended behavior changes distinct from editorial
  pruning, and route them through the owner's existing approval boundary.

Run the owning structural and generated-parity checks. Review admitted and
refused cases for meaning; a word count or valid file shape cannot prove it.
For a routing change, test a fresh reader with a representative task that does
not name the skill. Observe whether it loads the method before editing, and
record the prompt, load order and any missed branch in the existing work record.
