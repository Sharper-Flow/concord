# Changing things

Repair the mechanism that is wrong. A wrapper beside it, a special case inside
it, or a second path around it leaves the fault in place and adds a thing to
maintain. When a fix is shaped as adding another case to a rule, ask instead
whether the rule is right.

Before extending something you did not introduce, establish that it should
exist. State what it is for and what breaks without it. If you cannot say,
that is the finding. Something can be present, called, and working, and still
not be warranted.

Default to subtraction. When work retires or replaces something, remove now
what the approved plan retires, including law that requires it, through the
existing law-change route. Do not repair, extend, or conform to it
meanwhile. Remove its tests and coverage together, citing the retiring
clause. Keep proof for current obligations or name replacement proof. Never hide
superseded code behind a flag, guard, or compatibility path, and never copy an
implementation instead of calling it.

Within assigned work on Concord itself, CD-0243 D3 permits this retirement
without per-deletion operator approval, including Concord law and capabilities.
Record why and use an ordinary pull request with independent review and required
CI. Apply D3's exclusions and retained gates; never silently substitute the
item's agreed outcome. This exemption does not apply to other Products.

Choose dependencies by total engineering cost, not by minimizing dependency
count. Prefer a well-established, actively maintained dependency when it provides
a clearer, more reliable solution than custom code. Adding a suitable dependency
within the agreed scope is a normal engineering decision. Respect explicit
project constraints and existing security, legal, and spending approval
boundaries.

Delete records of what a thing used to be. A struck-through line, a renamed-from
note, a commented-out block, or a file kept only as a marker records history
that version control already holds. Material that still governs a decision is
not history, however old it is.

Never delete tests, validation, error handling, or observability to reduce the
size of something. A failing test is not a reason to delete it. Every test
deletion must cite the clause that retires its behaviour; review must check that
the clause covers the deletion.

Never use `git stash`: every worktree shares one stash list. When your role
may commit, save work as a commit on the item's own branch instead.

Commit only durable content. A committed comment or document states intent, a
constraint that is not obvious, or Product law. It carries only what the code,
types, tests, and generated contracts cannot show. Change history and
provenance, such as issue, pull request, and work identifiers, belong in
commits, pull requests, and work records. Write no diff labels, no account of
earlier versions of the code, and no reference to the conversation that
produced it.

Remove the bloat your change made. For other bloat, and for faults outside the
asked work, even nearby, file a follow-up item instead of widening the change.

Prefer the clearest result over the smallest edit. Do not build for needs that
have not arrived, and do not refuse necessary structural work to keep a diff
small.
