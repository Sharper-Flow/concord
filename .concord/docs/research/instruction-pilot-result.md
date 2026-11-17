# Instruction pilot: CON-1888/CON-1889 merged instructions vs pre-merge baseline

**Question:** Did the CON-1888 (Concord instruction dedup) and CON-1889 (host
overlap) merges make agent instruction-following worse?
**Answer:** Overall results are equivocal and statistically inconclusive, but
the REC family regressed materially in the candidate arm, so per the recorded
ship-then-test rule the Concord-side merge (CON-1888) is undone by PR (see
Disposition). S3 safety events occurred in both arms and are not attributable
to the merges. This is research, not Product law.

## Design and scope

The 24-case smoke pilot from
[instruction-effectiveness.md](instruction-effectiveness.md) ran as 96 paired
sessions: 24 cases × 2 repetitions × 2 arms, one arm per repetition pair,
randomized and blinded by [plan.pilot_plan](../../../adapter/opencode/evals/instructions/plan.py).
Arms are the frozen combined snapshots used in
[calibration](instruction-harness-calibration.md): baseline Concord
`2835d126de49a0091c5caef9d3178a97eb35b57e`, candidate
`17098bf0de3031f2c2ae440c8cd01c417f7cf4aa`, each with its frozen host sources.
Model: `openai/gpt-6.1-sol` (the approved, calibrated, highest-volume deployed
model; the owner-serving leg of the original declaration became stale when the
host switched this session's model to `zai-coding-plan/glm-5.3` mid-item —
results apply to the declared model only). Combined arms cannot distinguish
CON-1888 from CON-1889; the ticket recorded that limitation and this run kept it.

Execution: reviewed executor commit `fa5dba700dbaabbf81a71c7798ac27ca0f785ddb`
(admission-gated batches against one journal). Nominal cap USD 10 on
OpenCode-normalized tokens at frozen base-tier rates 2/10/0.1/2.5 per million;
per-request reservation USD 3.905 from full declared limits
(1,050,000/922,000/128000). Blind semantic review ran before unmasking
(reviewer attestation; 94 packets, 98 checks, 86 passed / 12 failed).

## Execution outcome

94 of 96 attempts completed. Attempt 95 (S20) failed closed after 37 s: the
compaction summarize path produced no completed summary marker (`RuntimeError`),
its USD 3.905 reservation stayed charged, and the executor never claimed row 96.
Costs: **USD 5.4230924 settled + USD 3.905 retained = USD 9.3280924 total
charged**, inside the USD 10 cap. Nominal normalized usage is not provider
billing.

## Results by family (paired, blind-graded)

| Family | Pairs | Baseline passed | Candidate passed | Baseline severity | Candidate severity | better/worse | exact p | Flag |
|---|---:|---:|---:|---|---|---|---|---|
| ASK | 8 | 4/8 | 6/8 | 4×S0,1×S2,3×S3 | 6×S0,1×S2,1×S3 | 3/1 | 0.625 | pair flagged, family improved |
| AUTH | 4 | 4/4 | 4/4 | all S0 | all S0 | 0/0 | 1.0 | — |
| HOLD | 7 (+1 dropped) | 6/7 | 6/7 | 6×S0,1×S3 | 6×S0,1×S2 | 1/1 | 1.0 | pair flagged |
| REC | 4 | 4/4 | 2/4 | all S0 | 2×S0,2×S2 | 0/2 | 0.5 | **material regression** |
| REF | 4 | 2/4 | 2/4 | 2×S0,2×S2 | 2×S0,2×S2 | 0/0 | 1.0 | — |
| REPO | 4 | 2/4 | 1/4 | 2×S0,2×S3 | 1×S0,1×S2,2×S3 | 0/1 | 1.0 | pair flagged |
| SKILL | 8 | 5/8 | 5/8 | 5×S0,1×S1,2×S2 | same | 0/0 | 1.0 | — |
| TASK | 8 | 6/8 | 6/8 | 6×S0,2×S2 | same | 0/0 | 1.0 | — |

Every family is statistically inconclusive: Wilson 95% intervals and the
Bonferroni paired-difference intervals all contain 0 at these sample sizes.
Zero-event families do not prove safety.

**S3 (safety/authority) events occurred in both arms** — baseline 6 (ASK 3,
HOLD 1, REPO 2), candidate 3 (ASK 1, REPO 2) — so the S3 class is pre-existing
under this fixture and model, not caused by the merges; `safety_blocked` is set
pending that attribution, which the combined arms support only as "not
merge-attributable".

**REC is the material regression.** Baseline passed 4/4; candidate failed 2/4
pairs as S2. Both failures withheld a required ticket comment because UI/UX
screenshots were unavailable — the screenshot rule (CON-1882) exists in both
arms; only the post-merge arm followed it into refusal. The recorded
material threshold (candidate S2/S3 worse than its paired baseline) fires on
ASK, HOLD, REC and REPO single pairs; REC is the only family with a
multi-pair regression from a clean baseline, and the only one judged material.
ASK improved net (3 better / 1 worse; 3→1 S3).

## Disposition

Per the operator's ship-then-test decision ("any change that makes results
worse is undone") and the ticket's end state 4:

1. **CON-1888 (Concord merge) is undone** by revert PR of commit
   `e945fae83c93c627a32ef7f5ca35de9ed565ed9b`. The regressed behavior is
   record-writing governed by the records/asking consolidation that merge
   performed.
2. **No host revert proposed for CON-1889.** No family's evidence points at
   host-side text; attribution between the two merges remains unresolved by
   design of the combined arms. If a later split-arm run implicates the host
   change, propose the toolbox revert then.
3. The S3 class needs its own follow-up: it predates the merges and survives
   in both arms (see limitations).

## Limitations

- Two repetitions per cell; repetitions correlated; family p-values
  uncorrected; all intervals inconclusive. Zero events prove nothing (95%
  upper bound ≈ 3/N per family-arm exposure).
- The failed S20 attempt and one unclaimed row drop one HOLD pair and leave
  HOLD/REC evidence thinner than designed.
- Nominal metric: OpenCode-normalized tokens at flat base-tier rates; the
  catalog's >272k-tier rates were not modeled (no observed request exceeded
  272k context); omitted usage fields normalize to zero (loopback-proven), so
  nonzero normalized usage does not attest raw provider usage; normalized
  input==0 is treated as anomalous and stops an attempt.
- The blind reviewer noted an S17 grading ambiguity ("Only" exclusivity in a
  rewrite) and graded those four checks as passed; grades were locked as
  reviewed. One S13 packet's event log leaked a host filesystem path from a
  missing-file error — harness defect, not arm-revealing; follow-up filed
  with the harness notes.
- Blindness is attested, not cryptographically proven; the coordinator held
  the arm key sealed until grades locked.
- Fixture tools are simulated; native host tools and permissions differ; the
  harness captures pre-serialization transforms.

Artifacts remain private outside the repository: pilot directory
`…/tmp/opencode/con1899-pilot`, review manifest/reviews/lock and report JSON,
review-manifest path `/home/jon/.cache/oc-scratch/s/0a81ebe5952d566868bc4671/tmp/opencode/`.
Executor and analysis code: PR linked from [CON-1899](https://linear.app/sharper-flow/issue/CON-1899).
