# R2: Proportional-Rigor Obligation Bands — Research Findings

> **Status:** Research complete; accepted by CD-0006 R2 on 2026-08-06.
> **Decision slot:** CD-0006 R2.
> **Question:** What concrete evidence obligations do independent maturity and
> audience-commitment bands require?

## Summary

Concord's independent-axes design (maturity + audience commitment) is the
canonical best-practice shape, validated by FIPS 199, FedRAMP, Microsoft SDL,
Google release stages, DO-178C, and risk-matrix literature. The combine formula,
global floor, and upward-only override are strongly evidence-backed.

## Combine formula (most evidence-backed element)

```
effective_rigor = floor ∪ max(maturity_obligation, audience_obligation)
```

- Look up maturity and audience obligations **independently** (two 1-D lookups).
- Take the **greatest** obligation per dimension (FIPS 199 "high-water mark").
- **AND-combine** every applicable component/resource policy (Odock "pass every
  scope"); each may only add, never remove.
- The global floor is the absolute lower bound.

Risk-matrix literature explicitly warns against a full 5×3 matrix: Cox (2008)
shows "poor resolution, range compression, worse than useless for
negatively-correlated axes."
Source: <https://onlinelibrary.wiley.com/doi/10.1111/j.1539-6924.2008.01030.x>

## Global minimum floor (applies to ALL work)

| Obligation | Concrete example | Borrowed from |
|---|---|---|
| Stated purpose + owner | One-line "what this is, who maintains it" | DO-178C DAL E; SemVer 0.x |
| At least one proof artifact | A passing test, runnable demo, or spike/findings doc | spike/PoC practice; FedRAMP Low |
| No silent weakening | Component/resource policy may only add controls | Odock "pass every scope" |

## Maturity bands

| Band | Core obligation | Concrete examples | Borrowed from |
|---|---|---|---|
| **prototype** | "Does what it claims" + change-anytime notice | Spike findings doc; PoC with setup/outcome/baseline; SemVer 0.x instability notice | DO-178C DAL E; SemVer 0.x |
| **alpha** | Functional verification + explicit no-reliance terms | Invitation-only; "no SLA, test-env only"; changelog exists | Google Alpha; SemVer -alpha |
| **beta** | Announced + defined support + graduation criteria | Public beta announcement; draft SLOs; coverage threshold; entry/exit criteria | Google Beta; Rex Black criteria |
| **production** | Reliability + compatibility + supported-surface commitment | Defined SLO + error budget; passed PRR; monitoring/alerting; rollback gates; backward-compat contract | SRE PRR; Google GA |
| **deprecated** | Sunset-bound + maintenance-only + migration path | Announced sunset date (Google: 180d; IBM: 12mo); security-only fixes; documented migration | Google/IBM lifecycle |

## Audience-commitment bands

| Band | Core obligation | Concrete examples | Borrowed from |
|---|---|---|---|
| **operator_only** | Asset-centric minimal threat model + internal access | Simplified threat model (assets/dataflows/threats/mitigations); internal authz; logging | Microsoft SDL-LOB; FedRAMP Low |
| **limited** | Opt-in terms + defined participant set + proportional review | Beta-program opt-in; feedback loop; security review scaled to limited-PII | Google beta programs; FedRAMP Moderate |
| **public** | Full threat model + public security/privacy review + assessment | Exhaustive threat model; privacy assessment; SLA commitment; independent assessment | Microsoft standard SDL; FedRAMP High |

## Confidence levels

- Combine formula, floor, upward-only: **high** (multiple authoritative sources converge)
- production, public, deprecated, beta bands: **high** (well-established practices)
- prototype, alpha, operator_only, limited bands: **medium-high** (judgment calls; flag for review after first real application)

## Sources

- DO-178C DAL scaling: <https://www.casa.gov.au/sites/default/files/2021-08/advisory-circular-21-50-approval-of-software-and-electronic-hardware-parts.pdf>
- Google Workspace testing phases: <https://support.google.com/a/answer/11202276>
- Google Cloud API versioning/deprecation: <https://cloud.google.com/apis/design/versioning>
- SemVer 2.0.0: <https://semver.org/spec/v2.0.0.html>
- Microsoft SDL-LOB: <https://learn.microsoft.com/en-us/previous-versions/windows/desktop/dd831971(v=msdn.10)>
- SRE Production Readiness Review: <https://sre.google/sre-book/evolving-sre-engagement-model/>
- Rex Black exit/release criteria: <https://rexblack.com/resources/writing/exit-and-release-criteria>
- Cox (2008) risk matrix critique: <https://onlinelibrary.wiley.com/doi/10.1111/j.1539-6924.2008.01030.x>
- NIST FIPS 199: <https://nvlpubs.nist.gov/nistpubs/FIPS/NIST.FIPS.199.pdf>
- NIST SP 800-53B: <https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-53B.pdf>
- FedRAMP Rev 5: <https://www.fedramp.gov/resources/documents/Rev-5-Transition-Overview-Presentation.pdf>
- Odock Policy Inheritance: <https://docs.odock.ai/docs/security-and-guardrails/guardrails/policy-inheritance/>
- IBM software lifecycle: <https://www.ibm.com/support/pages/ibm-software-support-lifecycle-policies>
- Spike/PoC evidence practice: <https://www.superdurszlak.dev/posts/research-driven-how-spikes-lay-ground-for-projects/>
