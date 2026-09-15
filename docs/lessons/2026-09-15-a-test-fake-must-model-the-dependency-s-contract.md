## What happened

`bin/oc-test` was reduced to one tier. The change passed the tier name `conformance` to `oc-test-gate` as its admission class. The deployed gate accepts `targeted`, `smoke`, and `full` and exits 2 on anything else, so the sole remaining tier -- and the exact invocation `.concord/tooling.v1.json` declares -- failed on every host that has the gate installed:

    START conformance
    WAIT admission: conformance (shared heavy-test slot)
    unknown tier: conformance
    FAIL conformance (exit 2)

The pull request passed 11 of 11 CI checks and merged. CI never runs this wrapper, so no check could see it.

## Why the test did not catch it

`scripts/test-oc-test.py` builds a fake `oc-test-gate` on a temporary PATH. The fake had been rewritten in the same change to accept `conformance`:

    if [[ "$1" != "conformance" ]]; then exit 9; fi

The fake was derived from the wrapper's new code rather than from the gate's contract. It therefore agreed with the wrapper about a value the real gate rejects. The test passed, the validator that requires script-test coverage passed, and the defect shipped.

## The rule

When a test fakes an external dependency, the fake must reproduce that dependency's contract. Its accepted inputs, its rejected inputs, and its failure mode all belong to the dependency, not to the caller. A fake written from the caller proves only that the caller agrees with itself.

The symptom to watch for: a change edits the code and its fake in the same commit so that they continue to match. That is not a test update, it is a guard weakened to admit the new behaviour.

## What the repair looked like

The fake now enumerates the real admission classes, exits 2 with the real `unknown tier` message otherwise, and records the class it received so a test can assert it. Reverting the one-word fix in `bin/oc-test` fails 3 of the 4 tests. Separately, `run_tier` now takes the reported tier and the admission class as two arguments, because conflating the two vocabularies is what allowed the defect.

## How it was found

The workflow's mandatory refinement pass, after the change had already merged. A review lane read the wrapper against the deployed gate rather than against the test.