---
name: concord-test-design
description: Design the tests a change needs, choose what each test must prove and at which seam, judge whether a planned test earns its place, or decide when no test is warranted.
---

# Test design

Use this method to choose tests that prove a change's observable outcomes. It
guides judgment and adds no approval or scope beyond the assigned work.

## Canonical references

- [Portable engineering method admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0246-concord-ships-portable-engineering-methods.md).

## Choose behavior and seam

Test observable behavior through public interfaces. A useful test reads like a
specification of a capability and survives a refactor that preserves it. Prefer
the highest public boundary that observes the promised outcome; use a lower
boundary only when the higher cannot observe it or costs too much. Load the
shipped `concord-codebase-design` skill for seam and depth vocabulary. If no seam
can hold a faithful test, record a design finding rather than write a shallow
test.

Give each behavior its own test and each independently testable slice an
independent test. Develop a vertical slice at a time instead of writing a batch
of tests against imagined implementation details.

## Prove the test

The shipped Completion instructions own when a test must be shown to fail
without the fix. What the method adds: the failure must be the expected one —
the behavior is missing, not a typo or a harness error — and a test written
after the change is proven by reverting the change, watching the test fail for
that reason, restoring the change, and watching it pass.

Assert real behavior, not the behavior of a mock. Avoid pinning private methods,
internal constants or bounds, mock call counts, or side channels: these can
break when the behavior has not changed. A tautological assertion recomputes the
expected value the same way as the implementation; instead derive it from an
independent worked example, known-good literal, or specification. The shipped
Completion instructions own the prohibition on absence-only tests.

For removals and non-executable changes, defer to the shipped Completion
instructions on when a test is not warranted.

## Run the checks

The Product's `.concord/tooling.v1.json` declares ready check commands; the
repository's contributing or testing documentation names its test admission
route. Run the new test and the surrounding suite through those routes, and
report each failure by name.
