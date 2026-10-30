# Native adapter acceptance recipe

[`native-acceptance.v1.json`](native-acceptance.v1.json) pins the repository's
transport, session-move, and native plugin-publication test harness. It uses the admitted
`bun+check` pair with Bun 1.4.0 and Python 3.13.7. No third-party package is
required: the host plugin API is mocked by `plugin-tool.fixture.ts`.

Regenerate the recipe and its explicit empty dependency lock with
`python3 scripts/generate-adapter-oracle.py`; check them with `--check`.
The generator owns both JSON outputs. Bun deletes empty locks during ordinary
installation, so use the generator to restore the native materializer's lock.
Generated TypeScript fixtures keep their existing generators.

For native preparation, pin `recipe_source` to this manifest at its committed
SHA. The control's `cwd` is `adapter/opencode`; its `case_ids` contains
`case:adapter-native`; its exact logical argv is:

```json
["check", "adapter-transport", "adapter-move", "adapter-publication"]
```

Execute against the actual returned `preparation_run_ref`. Preparation
checks availability without running acceptance bodies; it is not verification.
The pinned tests establish their declared baseline invariants, not acceptance
of untested future requirements or proof of an installed host's behavior.
Extend and re-pin the harness when acceptance requires new assertions. Native
verification must support the recipe pair; never bypass its readiness checks.
