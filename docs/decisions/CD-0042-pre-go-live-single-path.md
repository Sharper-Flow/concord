# CD-0042: Pre-go-live development keeps one agent-surface path

- **Status:** Accepted
- **Date:** 2026-08-19
- **Scope:** Agent-tool contract identity, pre-go-live change evidence, obsolete-path
  removal, and the boundary that activates compatibility policy; issue #195
- **Approval:** Operator directed that Concord has no go-live compatibility obligation,
  does not need agent-surface versions yet, and must delete code outside the primary
  path on 2026-08-19; the public record is
  [issue #195 comment](https://github.com/Sharper-Flow/concord/issues/195#issuecomment-5347857526)
- **Related:** CD-0005, CD-0007, CD-0010, CD-0024, CD-0031, CD-0041, TS1,
  TS5–TS9
- **Amends:** CD-0005 D6/D8/D9 and implementation acceptance; CD-0024 D3/D4;
  CD-0031's agent-surface identity; CD-0041's pre-go-live surface sequencing; TS7,
  TS8, and TS9
- **Preserves:** One generated canonical manifest and digest; strict schemas; signed
  client bootstrap; fail-closed unknown input; durable Product/event authority; release
  automation
- **Supersedes:** Pre-go-live agent-surface SemVer, range negotiation, compatibility
  windows, deprecation metadata, supported-model release trials, and replay/upcast code
  retained only for unreleased surface revisions

## Context

Concord publishes development releases, but it has not gone live. No external user,
supported client cohort, or compatibility promise depends on an old agent-tool
surface. The repository nevertheless treated each development edit as a production
contract migration: it assigned semantic surface versions, negotiated ranges, kept
old adapter/core cases, described deprecation windows, and made supported-model trials
a release gate.

That machinery preserved revisions nobody can depend on. It also made the next
primary-path correction conditional on infrastructure built to compare obsolete
development surfaces. This is the wrong trade before go-live. Git history already
records replaced development designs; runtime code should express the accepted design.

## Decision

### D1. Go-live is an explicit Product boundary

Concord remains pre-go-live until an accepted operator decision names the first
supported release, clients, and compatibility cohort. A GitHub release, tag, installer,
or local database does not cross that boundary by itself.

Before that decision, repository history is evidence of earlier development. It does
not create a runtime compatibility obligation.

### D2. The current agent surface has one identity

The canonical generated manifest and its digest define the only agent-tool contract.
The adapter, grant, invocation, session boot, and result envelope bind that exact
digest. They do not negotiate a semantic surface version, version range, envelope
version list, previous client shape, or removal window.

Unknown or mismatched digests fail before a domain effect. There is no fallback,
down-conversion, alias, or version-dependent meaning.

Schema and event format versions remain where they are needed to decode a current
authoritative structure. Release versions remain build and distribution identifiers.
Neither creates an agent-surface compatibility promise before go-live.

### D3. Superseded development paths are deleted

When the accepted pre-go-live contract changes, the same change removes the replaced
surface fields, negotiation branches, compatibility tables, migration hints, replay
upcasters, aliases, tests, and prose that exist only for the old development shape.
The primary path is changed directly.

Internal readers stay only when current authoritative Product or event data requires
them. A historical test fixture, released development tag, or hypothetical old client
is not enough to keep runtime code.

### D4. Deterministic evidence gates pre-go-live changes

TS1 scenarios, strict generated schemas, authority and transaction tests, negative
probes, conformance, and repository checks gate the current pre-go-live surface.
Supported-model comparisons may inform design, but they do not block a pre-go-live
release and are not a substitute for deterministic authority evidence.

TS9's supported-model population, paired-trial, telemetry, and deprecation gates become
active only after the go-live decision defines supported populations and a real current
contract. CD-0024's one-time exception is closed historical evidence, not a template
and not a gate that needs another exception.

### D5. Domain-overlap control lands on the primary path

Issue #195 adds `concord_work_relate.resolve_overlap` and typed `domain_overlap`
recovery directly to the generated current manifest. The accepted `AJ5-resolve-domain-overlap`
scenario and its real-dispatch binding prove the missing authority path and the new
outcome. No `5.0.0` cutover, old-client matrix, or model-trial runner is part of this
change.

### D6. First go-live must establish future evolution law

The go-live decision must name:

- the supported client and model populations;
- the contract identity exposed as a compatibility promise;
- whether semantic versions add information beyond the manifest digest;
- migration and support periods, if any; and
- the evidence required to add, change, or remove a supported operation.

Until then, no code or prose may prebuild those policies as if users already depend on
them.

## Consequences

- Grant and invocation authority becomes smaller: one digest match, no range parser.
- Generated artifacts stop carrying an artificial surface version.
- Result and session packets identify the generated manifest without a parallel
  surface-version field.
- Pre-go-live surface changes still require named scenarios and operator acceptance
  when they alter Product authority or consequence.
- Historical decision records remain readable, but living contracts describe only the
  current path.
- Issue #196 no longer needs a TS9 runner before replacing Epic with Initiative; it
  still needs deterministic scenario and authority evidence.

## Rejected alternatives

**Keep one hard-coded semantic version.** Rejected because it leaves negotiation and
compatibility concepts in the primary model without a supported cohort.

**Approve another TS9 exception.** Rejected because there is no pre-go-live release gate
to except. The gate activates when go-live creates a population to measure.

**Delete every version field in the repository.** Rejected because schema, event,
workflow, and release versions can identify real formats or builds. This decision
removes agent-surface compatibility machinery, not structural decoding information.

## Verification

- The agent manifest has no semantic surface version or deprecation section.
- Generated Go and TypeScript contracts expose one manifest digest and no
  `ManifestVersion`/`manifestVersion` constant.
- Signed grant assertions, persisted grants, invocations, session boot, and result
  envelopes bind the digest without surface-range or envelope-version negotiation.
- No runtime SemVer parser, old agent-surface replay upcaster, or old/new compatibility
  matrix remains.
- `AJ5-resolve-domain-overlap`, all PM1/TS1 deterministic scenarios, generated-contract
  checks, full tests, and conformance pass.
