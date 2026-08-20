# Skills

Concord ships no skill, and this directory stays reserved.

[CD-0043](../docs/decisions/CD-0043-host-owned-lane-methodology.md) D3 closed the
question this directory used to hold open. Lane methodology — review dimensions,
verification rubrics, the inspection method for a rendered surface — is host-owned.
It reaches a worker only through the enumerated `CONCORD_HOST_INSTRUCTIONS` surface,
bound by content hash into the CD-0034 dispatch provenance manifest. It is never
durable coordination state, so it is never a Concord skill.

The directory remains packaged into releases so a later accepted decision can
populate it without changing release shape. Its emptiness is a decided state, not an
unfinished one.

Reopening requires a decision that names the consumer, the versioning regime, and
why the provenance-recorded host surface is insufficient.
