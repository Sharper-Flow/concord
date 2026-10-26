# Skills

[CD-0245](https://github.com/Sharper-Flow/concord/tree/6d9bcd19902a771b047bc54059d49516d70b98d8/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md)
D1 admits a skill when it operates a structure that accepted Concord law defines,
and Concord's checks verify the result. The first such skill is Domain-shape
stewardship: migration, code-root reorganisation, fact refinement, and gap audit.

Load [Domain-shape stewardship](concord-domain-shape-stewardship/SKILL.md) for
initialization, migration and reorganisation, refinement, or audit.

A shipped skill lives here, installs under the versioned data root through the
[CD-0063 D2 route](https://github.com/Sharper-Flow/concord/tree/6d9bcd19902a771b047bc54059d49516d70b98d8/.concord/docs/decisions/CD-0063-shipped-operator-conduct-rules.md#d2-the-installation-is-central-a-project-holds-pointers),
and changes only with a release. No separate skill-versioning regime exists.
It calls only Concord operations and the commands the Product declares in
`.concord/tooling.v1.json`, and names no machine, private repository, or host tool.

Lane methodology stays host-owned under
[CD-0043 D1 and D2](https://github.com/Sharper-Flow/concord/tree/6d9bcd19902a771b047bc54059d49516d70b98d8/.concord/docs/decisions/CD-0043-host-owned-lane-methodology.md).
A skill that tells a worker how to review, verify, or implement inside its lane
is not admitted. Methodology reaches a worker through the enumerated host
instruction surface, bound by content hash into dispatch provenance.

Admission does not establish implementation or conformance. A shipped skill
never substitutes for a check and holds no independent evidence, approval, or
law authority.
