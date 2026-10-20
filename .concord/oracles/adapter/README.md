# Adapter dispatch acceptance recipe

`dispatch-acceptance.v1.json` pins the repository's native plugin publication,
dispatch/result, and authorization-window test harness. It uses the admitted
`bun+check` pair with Bun 1.4.0 and Python 3.13.7. No third-party package is
required: the host plugin API is mocked by `plugin-tool.fixture.ts`.

Regenerate the recipe and its explicit empty dependency lock with
`python3 scripts/generate-adapter-oracle.py`; check them with `--check`.
The generator owns both JSON outputs. Bun deletes empty locks during ordinary
installation, so use the generator to restore the native materializer's lock.
Generated TypeScript fixtures keep their existing generators.

For native preparation, pin `recipe_source` to this manifest at its committed
SHA. The control's `cwd` is `adapter/opencode`; its `case_ids` contains
`case:adapter-dispatch`; its exact logical argv is:

```json
["check", "adapter-dispatch", "adapter-guards", "adapter-publication"]
```

Use the actual returned preparation reference in the recorded job. Preparation
checks availability without running acceptance bodies; it is not verification.
The pinned tests establish their declared baseline invariants, not acceptance
of untested future requirements or proof of an installed host's behavior.
Extend and re-pin the harness when a job requires new assertions. The adapter
must also support the recipe pair before it can dispatch that job; never bypass
its readiness checks.

`TestAdapterDispatchRecipeGenerated` checks regeneration in every store test
environment. `TestAdapterDispatchRecipeRecordsReady` reads the committed recipe,
prepares it, records the exact ready job, and executes the pinned checks. Its
native portion requires the exact pinned runtimes and reports a skip otherwise.
