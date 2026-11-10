# Instruction evaluation harness

This harness tests synthetic instruction-following cases. Offline fixture tests
make no model calls; private calibration runs up to six claimed attempts per
artifact directory, including failed and pending claims. This is not a global
limit. The harness has no full pilot execution route, and calibration is not
evidence of effectiveness.

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
packet for that review; give reviewers only that packet, not the result file
or other artifacts. The runner has no command that records review results, so
calibration grades keep their semantic checks pending. HOLD cases
require a recorded compaction boundary and a resume read.

`plan.pilot_plan(seed, baseline, candidate)` accepts snapshots already verified
with `snapshots.load`. The seed must be a nonnegative integer of at least 128
bits. The snapshots must have distinct identities, Concord references and host
references. The function returns a blinded schedule and a separate
arm key, not running sessions. Keep both outside the repository and the key
away from graders until grades are locked.
