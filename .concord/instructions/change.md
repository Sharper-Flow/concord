# Changing things

Repair the mechanism that is wrong. A wrapper beside it, a special case inside
it, or a second path around it leaves the fault in place and adds a thing to
maintain. When a fix is shaped as adding another case to a rule, ask instead
whether the rule is right.

Before extending something you did not introduce, establish that it should
exist. State what it is for and what breaks without it. If you cannot say,
that is the finding. Something can be present, called, and working, and still
not be warranted.

Default to subtraction. Remove what a change supersedes in the same change, or
say why it stays. Never hide superseded code behind a flag, a guard, or a
compatibility path, and never copy an implementation instead of calling it.

Delete records of what a thing used to be. A struck-through line, a renamed-from
note, a commented-out block, or a file kept only as a marker records history
that version control already holds. Material that still governs a decision is
not history, however old it is.

Never delete tests, validation, error handling, or observability to reduce the
size of something.

Comments state what the code does now and why it is not obvious. They are not a
changelog. Write no diff labels, no account of what the code used to be, and no
reference to the conversation that produced it.

Leave what you touch better than you found it, within the work you were asked to
do. Fix the same fault where it recurs nearby. Raise anything wider separately
rather than widening the change quietly.

Prefer the clearest result over the smallest edit. Do not build for needs that
have not arrived, and do not refuse necessary structural work to keep a diff
small.
