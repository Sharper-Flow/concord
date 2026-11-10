# Instruction harness calibration

**Question:** Can a full smoke pilot have a harness and a measured nominal cost?
**Answer:** Yes, with substantial uncertainty. A harness exists and six real
calibration sessions provide measured usage and a nominal API-equivalent cost.
They do not establish the full pilot's cost, effectiveness or safety. No full
pilot has run. The operator question about the pilot remains open.

This is research, not Product law or an adopted change. The
[original design](instruction-effectiveness.md#evaluation-design-not-run)
defines 24 synthetic cases, two repetitions and two arms: 96 sessions per
model. The [harness](../../../adapter/opencode/evals/instructions/README.md)
implements calibration and offline estimates, but has no full-pilot execution
route. The harness source is the implementation reference.

## Scope and evidence

The frozen baseline represents the sources before CON-1888 and CON-1889; the
candidate represents the sources after those changes. Their public Concord
source revisions are, respectively,
`2835d126de49a0091c5caef9d3178a97eb35b57e` and
`17098bf0de3031f2c2ae440c8cd01c417f7cf4aa`. The corresponding host source
references are retained in the private snapshot manifests. Snapshots and raw
session logs remain outside the public repository. The supplied calibration
record is the source for the observations below; the private artifacts cannot
be independently inspected from this public note.

The sessions used OpenCode 1.18.35 and the recorded model ID
`openai/gpt-6.1-sol`. That ID fills both declared model roles: it appeared in
13 of 14 assistant messages in the owner's current session and accounted for
129,615 of 423,669 messages (30.6%) in one local shard's rolling 30-day
aggregate. The aggregate is a private observation, not a public census. The
recorded model ID is not proof of the serving backend. No second model was
evaluated.

Exactly six real sessions ran: baseline and candidate for each of S01, S05 and
S19. No other model evaluation or pilot ran. All six deterministic grades were
S0. An independent blind semantic reviewer, `ses_e3b5f9b5cffeB5G5u17CYuDeKm`,
passed all semantic checks. Candidate skill loads were unavailable and S01 hit
the step limit. These observations are calibration evidence, not proof of
general instruction effectiveness or pilot results.

The fixture is synthetic and does not provide arbitrary shell or the built-in
skill tool; unknown skill attempts were observed. The harness captures system
and message transforms before provider serialization, not the provider wire
request. Native tools, permissions and loading order can differ from the
fixture. The restricted Go execution used for S15 can produce false negatives;
see the [harness limitations](../../../adapter/opencode/evals/instructions/README.md#interpretation-and-pilot-planning).

The supplied offline suite result is 49 passing tests; an earlier run reported
47 before a later fix. These offline results do not add model sessions or prove
pilot effectiveness.

## Observed usage and nominal cost

| Arm | Input | Output | Reasoning | Cache read | Cache write | Wall seconds | Nominal USD |
|---|---:|---:|---:|---:|---:|---:|---:|
| Baseline | 81,179 | 1,085 | 87 | 270,592 | 0 | 125.413876 | 0.2011372 |
| Candidate | 80,571 | 1,150 | 74 | 267,776 | 0 | 127.964981 | 0.2001596 |
| Combined | 161,750 | 2,235 | 161 | 538,368 | 0 | 253.378857 | 0.4012968 |

Cached provider metadata observed on 2026-11-10 supplied nominal USD rates per
million tokens: input $2, output $10, cache read $0.10 and cache write $2.50.
Output and reasoning were priced separately using the output rate, as recorded
by the host. These rates and token counts are not billing proof. The host
reported billed cost zero; actual subscription cost is unknown.

| Case | Baseline USD / seconds | Candidate USD / seconds |
|---|---:|---:|
| S01 | 0.0532484 / 42.969447 | 0.0523996 / 42.147526 |
| S05 | 0.0863768 / 33.686536 | 0.0859108 / 35.115021 |
| S19 | 0.0615120 / 48.757894 | 0.0618492 / 50.702434 |

Case and arm wall times are rounded independently to six decimal places, so
the displayed baseline case times differ from the arm total by one microsecond.

## Pilot projection and decision boundary

Multiplying the highest observed session cost, $0.0863768, by 96 gives
$8.2921728 nominal for one model. A $10.00 administrative cap is proposed, not
approved or enforced by the harness. This extrapolation is not a statistical
or actual spending guarantee; provider retries were not observed or bounded.
Multiplying the maximum observed session wall time, 50.702434 seconds, by 96
gives 4,867.433664 seconds, approximately 81.1 minutes serially. That excludes
grading, labor, queues and retries.

The estimate remains incomplete: AUTH, REC, SKILL, TASK and REPO have no
calibration samples, and overall precision is inconclusive. The
[estimator](../../../adapter/opencode/evals/instructions/estimate.py) uses
observed family means where available and substitutes the observed maximum for
unsampled families in a provisional projection; neither substitution proves a
bound. No mean-based projection is a reliable headline for this sample.

If a pilot proceeds, any S3 event or material family regression means undoing
the responsible merge. Zero observed failures in these six sessions does not
establish safety. The proposed cap and this calibration do not authorize the
pilot or settle the pending operator question.
