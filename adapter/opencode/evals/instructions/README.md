# Instruction evaluation harness

This harness tests synthetic instruction-following cases. Offline fixture tests
make no model calls; private calibration runs up to six claimed attempts per
artifact directory, including failed and pending claims. This is not a global
limit. The separate pilot route runs the blinded schedule under a nominal ceiling.
Calibration is not evidence of effectiveness.

## Offline tests

From the repository root, run:

```sh
bin/oc-test broad -- python3 -m unittest discover -s adapter/opencode/evals/instructions -t . -p 'test_*.py'
```

The suite calls Go for a restricted fixture check. It needs a local `go`
command, version 1.22 or later, and never downloads a toolchain
(`GOTOOLCHAIN=local`). It is not hermetic with respect to the installed runtime
or Go cache. Restricted Go execution can produce false negatives unrelated to
the submitted code; inspect failures before drawing conclusions.

## Snapshots and calibration

Use `snapshots.freeze(destination, repository, role_file=..., instruction_files=...,
concord_ref=..., host_ref=...)` from Python to create a write-once snapshot;
optional inputs include `agents_file`, `host_files`, and `catalog_file`. Source
files may be in the repository, but the destination must be outside it. Supply
accurate references and no secrets: the snapshot stores original content and
source paths. `snapshots.load(path, repository)` verifies the snapshot before use.

For calibration, use OpenCode 1.18.35 and an authenticated provider/model. The
runner keeps OpenCode's internal plugins, including the Codex authentication
plugin that serves OpenAI OAuth. It reuses the caller's OpenCode data directory
(`XDG_DATA_HOME`), which holds stored provider authentication, so calibration
sessions are also written to that data store. Provider calls may incur charges.
The rates JSON must contain a `source` and `rates` object with `input`,
`output`, `cache_read`, and `cache_write` rates per million tokens. The runner
does not check that the rates belong to the chosen model. Choose case IDs from
`cases.py`. From `adapter/opencode/evals`:

```sh
python3 -m instructions.run calibrate --snapshot /outside/snapshot.json --artifacts /outside/calibration --model provider/model --cases S18 --rates-json /outside/rates.json
python3 -m instructions.run estimate --artifacts /outside/calibration --proposed-cap 1
```

The snapshot, rates file and artifact directory must be outside the repository.
Protect artifacts: captures and the reused OpenCode data store may contain
sensitive text. Do not put secrets in snapshot sources, captures, or logs. The
runner discards server standard output, keeps standard error in each attempt's
`host-stderr.log` without lines that look like credentials, and withholds
exception text. It cannot guarantee that captured messages contain no secrets.

The ledger charges a slot before each attempt. Each requested case runs once: a
request larger than the remaining slots is refused before any session starts,
and a failed attempt keeps its charge and is not retried. Do not estimate while
calibration is running. The estimate reconciles each ledger claim with any
result, including pending or failed attempts, and reports missing attempt IDs
and unsampled families. Eight case families and six attempts mean an estimate
from one directory is always incomplete. A provisional projection uses observed
family means and substitutes the observed maximum for unsampled families. That
`unsampled_family_bound` is an extrapolation, not a guaranteed upper bound. The
maximum-observed projection is available when there is an accounted sample.
The nominal API equivalent is not subscription billed spend; the proposed cap
is administrative, not enforced, and host limits do not bound provider retries.
Estimates assume one model per artifact directory and sequential sessions.

## Interpretation and pilot planning

The runner supplies the frozen role as the agent prompt and frozen instructions
and catalog as separate configured instruction files. OpenCode may load these in
a different order from an ordinary session. The plugin captures the complete
system and message transforms before provider serialization, not the provider
wire request or proof of exact serialized parity. The fixture tool simulates
actions; native host tools and their permissions differ. Automated checks score observable events and
do not read the final response; semantic checks require independent manual
review before a pass. Each completed attempt writes an `<attempt>-blind.json`
packet for that review; keep result files and other artifacts from reviewers.
Calibration grades keep their semantic checks pending. HOLD cases
require a recorded compaction boundary and a resume read.

`plan.pilot_plan(seed, baseline, candidate)` accepts snapshots already verified
with `snapshots.load`. The seed must be a nonnegative integer of at least 128
bits. The snapshots must have distinct identities, Concord references and host
references. The function returns a blinded schedule and a separate
arm key, not running sessions. Keep both outside the repository and the key
away from graders until grades are locked.

## Pilot execution

The experimental pilot needs independent review before any paid run. Use one
private artifact directory for the entire pilot; never divide the schedule into
directories to reset its journal. The runner creates a randomized 96-row schedule
and a separate arm key, then claims rows before running them sequentially. Keep the
key and other artifacts away from blind graders. A failed or pending claim stops
the pilot and is never retried. The command exits nonzero while incomplete.

The limits JSON must identify the selected model and source and provide positive
integer `context`, `input`, and `output` limits. The rates JSON has the same shape
as calibration. For example, these are synthetic values, not model guidance:

```json
{"model":"provider/model","source":"synthetic","limits":{"context":10000,"input":9000,"output":1000}}
```

From `adapter/opencode/evals`:

```sh
python3 -m instructions.run pilot --baseline /outside/baseline.json --candidate /outside/candidate.json --artifacts /outside/pilot --model provider/model --limits-json /outside/limits.json --rates-json /outside/rates.json
```

The runner freezes model identity, supplied rates and limits, snapshot identities,
and harness hashes. It reserves the full declared context at the highest input or
cache rate plus the output limit at the output rate before each `chat.params`
request. The pilot-wide nominal ceiling is USD 10. Unknown and failed request
reservations remain charged. A completed, matching assistant message can settle
only the most recent reservation for its message ID; zero or invalid usage stops
the attempt. The ceiling can leave the pilot incomplete.

Nominal amounts use OpenCode's normalized token fields, not provider billing. The
loopback probe observed that OpenCode can normalize omitted usage fields to zero;
nonzero normalized usage does not attest the provider's raw response. The runner
cannot guarantee subscription spend or provider charges. The host retains the
calibration output-token setting and disables title generation for both arms.

The opt-in `native_budget_probe.py` exercises OpenCode 1.18.35 against a synthetic
loopback provider. Its artifacts must also be outside the repository. It is a
hook-observation probe, not a spending guard or proof of provider billing.

## Pilot analysis

Stop the pilot before analysis. A coordinator maps each completed attempt's
blind ID from the journal to its `<attempt>-blind.json` packet, then gives the
independent reviewer the packet, blind ID, and SHA-256 of the exact packet bytes.
Do not give the reviewer the journal, result files, schedule, metadata, snapshots,
or arm key. Keep the key hidden until the reviews are locked. The reviewer
attests to independence and blindness; the lock cannot prove either claim.

Create a private review JSON outside the repository. Include exactly one entry
for every completed attempt, keyed by blind ID. For example, replace the
illustrative ID, hash and check name with values from a real packet:

```json
{
  "reviewer": "independent reviewer",
  "independent_blind": true,
  "reviews": {
    "blind-id": {
      "packet_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
      "semantic_checks": {
        "check_name": {"verified": true, "passed": true}
      },
      "severity_note": "Reason for the review."
    }
  }
}
```

Review every name in the packet's `semantic_checks`; use an empty object when
there are none. Each check needs a boolean `passed` and `verified: true`;
each review needs a nonempty severity note. A pending review cannot pass. From
`adapter/opencode/evals`, lock the reviews before unmasking, then report:

```sh
python3 -m instructions.analysis lock /outside/pilot /outside/reviews.json /outside/review-lock.json
python3 -m instructions.analysis report /outside/pilot /outside/review-lock.json
```

The lock is write-once and binds the stopped pilot files and reviewed packets.
Both commands require private paths outside the repository; protect their output.
The report counts completed pairs and drops incomplete pairs from paired
comparisons. It separates ungraded attempts from failures and settled nominal
costs from unresolved reservations. Any S3 in either arm blocks safety; a
candidate S2 or S3 worse than its paired baseline requires attribution.

Two repetitions give low power, and zero events do not prove safety. Wilson
intervals and the Bonferroni paired difference interval are approximate. The
paired test treats correlated repetitions as independent, and family p-values
are not corrected for multiple families. Combined arms do not identify which
instruction change caused a difference. The report makes no automatic revert
or success decision; safety and regression flags require manual attribution.
