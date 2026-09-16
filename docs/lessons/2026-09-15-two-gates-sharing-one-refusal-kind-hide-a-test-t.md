Context. A worktree removal gained a Project-scope guard. Two lane attempts delivered tests for it. Both were rejected.

The second attempt's tests placed a live session in the target directory and left the scope field unset, then asserted the refusal kind. Both the pre-existing occupancy gate and the new scope gate emit KindWorktreeOwnershipConflict, and the occupancy gate runs first. The tests passed whether or not the scope guard existed.

Detection. Disable the guard under test and re-run. A test that still passes does not exercise it. This costs one edit and one test run, and it is the only cheap way to tell a real check from a decorative one when refusal kinds are shared.

Repair. Give the case a population that cannot trip the earlier gate: an observation that does not occupy the target. Then the new guard is the only thing that can produce the refusal.

General rule. Derive the check from the obligation, not from what the existing code happens to emit. A shared error kind makes two different failures indistinguishable to an assertion, and the weaker one will satisfy it.