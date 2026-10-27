---
name: concord-diagnosis
description: Diagnose a defect or performance regression whose cause is not yet proven. Use when something is broken, throwing, failing, flaky, or slow and the evidence already held does not show why.
---

# Diagnosis

Use this method when the cause of a defect is uncertain. A defect whose cause
the evidence already shows needs no ceremony: fix it and add the regression
test. The method supplies no approval and grants no permission to change source,
configuration, or live state; the work's scope and accepted law still decide
what you may touch.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Defect intake and recurrence](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0211-defect-intake-classifies-recurrence-before-fix.md):
  in Concord-managed work, the defect's capture and recurrence classification
  follow the Product's intake rules, not this skill.

Redact every secret before you show a command, its output, or a captured
artifact. Quote only the lines that carry the signal.

## 1. Build a signal that goes red

Find one command that drives the defect's code path and fails on the reported
symptom: a failing test at the seam that reaches it, a scripted request, a
command run on a fixture, a replayed captured input, or a comparison of two
versions. Run it and keep its output. Until this command exists, do not form a
theory from reading code.

Make the signal sharp, fast, and deterministic: assert the exact symptom, not
"did not crash"; pin time, seeds, and external inputs. For an intermittent
defect, aim for a higher reproduction rate, not a clean reproduction, and
record the rate.

If you cannot build a signal with the access you have, stop. Report what you
tried and the access, artifact, or instrumentation that would make the defect
reproducible. Do not guess a fix.

## 2. Reproduce and minimise

Confirm the signal fails with the reported symptom, not a nearby failure; a
different failure leads to a different fix. Then remove inputs, callers,
configuration, and steps one at a time, rerunning after each cut, until every
remaining element is needed to fail.

## 3. Hypothesise

List the plausible causes before testing any of them, ranked by likelihood.
State for each the prediction that would refute it: "if X causes it, changing Y
makes the failure disappear." A cause without a prediction is not yet a
hypothesis. Share the ranked list when someone with context can re-rank it
cheaply; do not wait for an answer you do not need.

## 4. Probe

Each probe tests one prediction and changes one variable. Prefer a debugger or
a targeted observation at the boundary that separates two hypotheses over broad
logging. Tag temporary instrumentation with one unique marker so its removal can
be checked. For a performance regression, measure a baseline first, then
compare against it.

## 5. Fix and lock it down

Turn the minimised case into a regression test at a seam where it exercises the
real call path. Watch it fail, apply the fix, and watch it pass. Then rerun the
original signal. If no seam can hold a faithful test, record that as a finding
about the design rather than adding a shallow test that cannot fail on this
defect.

## 6. Clean up and report

Remove the tagged instrumentation and throwaway harnesses. Record, in the
work's existing record, the confirmed cause, the signal, the regression test or
the missing seam, and any hypothesis left unrefuted. A cause you did not confirm
stays labelled as a hypothesis.
