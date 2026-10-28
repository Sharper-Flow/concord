# Documentation registry recipe

`documentation.v1.json` uses the existing `none+check` native runner with Python
3.13.7. It checks registry structure, metadata, evidence paths and the hashes
binding registered knowledge documents to their records. It validates the whole
knowledge corpus, not just the edited document.

Pin `recipe_source` to this manifest at a committed SHA. Use control `cwd` of
`.`, case `case:documentation-registry` and the exact logical argv:

```json
["check", "knowledge-index"]
```

The harness pins the validator and its imported helpers. Candidate documents,
record shards and other data remain ordinary snapshot inputs, not pinned files:
pinning them would replace the candidate edits with the recipe commit's bytes.
Re-pin the recipe when changing its validator closure or required Python runtime.

Preparation qualifies availability without running acceptance. Record the actual
returned preparation reference in the worker job. `TestDocumentationRecipeRecordsReady`
proves ready-job admission and native execution, including failure for a candidate
document with a stale hash and success after its hash is regenerated. Its native
portion requires the pinned Python runtime and reports a skip otherwise.
CI provisions Python from the adapter recipe's pin; keep those runtime pins
aligned and confirm this test passes rather than skips after a re-pin.

This control does not prove citation correctness or preserve a document's meaning.
Required CI runs the existing document-contract and link checks in a Git checkout;
Review judges the factual change. `check-doc-contract.py` needs Git inventory and
history that native snapshots do not contain. Its report-only or unregistered-draft
modes are not substitutes for those checks on registered records.
