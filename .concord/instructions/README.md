# Conduct corpus

These files are Concord's agent conduct rules. They are installed once and read
by every project that installs Concord.

[CD-0063](../docs/decisions/CD-0063-shipped-operator-conduct-rules.md) governs
what belongs here. Conduct is how an agent addresses the operator and what it
must establish before it asserts. It is not methodology: how a reviewer chooses
what to look at, or how a verifier chooses which commands constitute proof,
stays host-owned under CD-0043 D1 and is never shipped.

A rule enters this directory only if it is true without reference to a tool, a
machine, a repository, or a predecessor. A rule that names a command, a file
path, or a product is not conduct, and belongs to whichever surface owns that
subject. Rules for building Concord itself live in the repository `AGENTS.md`
and are not repeated here.

These files carry no authority over Product law. A decision record, a
specification, or a constitutional document outranks anything written here.
