# Isolated native issue-key resume

`scripts/native-issue-key-resume.py` drives the installed OpenCode server
through its session API and a scripted loopback provider. The candidate's actual
plugin and CLI run unchanged except for the installer's release-path stamp in
the disposable adapter copy. The observer cannot move a session, claim a
worktree, or replace a core result.

Supply an absolute installed host path, a prebuilt candidate CLI, a fresh run
root outside Git, and a directory containing already materialized public host
`node_modules` for that host version. The script installs nothing and copies no
existing configuration. Build the CLI from the same candidate before running:

```sh
go build -o "$ARTIFACTS/concord" ./cmd/concord
bin/oc-test targeted -- python3 scripts/test-native-issue-key-resume.py
bin/oc-test broad -- python3 scripts/native-issue-key-resume.py \
  --host "$INSTALLED_OPENCODE" --cli "$ARTIFACTS/concord" \
  --host-deps "$PUBLIC_HOST_PACKAGES" --run-root "$ARTIFACTS/fresh-run" \
  --budget-seconds 210
```

Use the host's workload runner when required. The explicit whole-experiment
budget includes setup. Each subprocess/API request is bounded. The fixture owns
and reaps the server process group and loopback provider in `finally`.

The operator CLI establishes only synthetic Product, Project, work, and issue
identity through the supported setup/invoke routes. No SQLite write seeds
authority. Read-only snapshots observe work, claims, events and occupancy. A
local bare origin keeps Git fetches offline. HOME, XDG roots, config, store and
temporary files are disposable; the host gets an environment allowlist, no
inherited credentials, no MCP, no ancestor project config, and one fake provider.
The candidate's five generated lane identities and the synthetic `fixture.md`
primary are staged into that isolated home. Neither is installed globally.

Startup waits for a bounded HTTP health response, not just a listening socket.
Native message readback joins only the requested session and user turn; the
prompt response's final assistant alone does not contain earlier tool steps.
After a metadata-only move, the fixture reads native tool context and shell
placement through the original entry directory. Only an actual target context
and the named retry recovery permit one replay of the unchanged selector.

Exit 0 requires the selected work ID, exact target, actual session, host and
native-shell readbacks, occupancy, post-landing CLI readback, unknown-key
refusal without claim/movement, and preservation of a live owner's work.
Exit 2 is a demonstrated supported-host boundary, **not** native proof. Exit 1
is an unexpected failure. Inspect `result.json`, `observer.jsonl`, `api.jsonl`,
`commands.jsonl`, `provider.jsonl`, `readiness.jsonl`, `host.log`, and `cleanup.json` under the run
root. CLI selection alone, a metadata move alone, or a completed native tool
card alone cannot produce a pass.

On a blocked host, do not replace the host client, stub claim/landing, change
permissions to bypass refusal, patch the launcher/core, or claim D11 success.
This experiment proves neither P3 history removal nor D13 migration.
