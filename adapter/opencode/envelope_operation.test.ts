import { test, expect } from "bun:test"
import { manifestDigest } from "./generated-contracts"
import { envelopeSchema, validateGeneratedEnvelope } from "./generated-contract-tests"

// The pairs are read out of the shipped envelope schema rather than listed
// here, so an operation added to $defs/toolOperation is covered by
// construction instead of by remembering to extend this file.
const writeTools = new Set(["concord_work_define", "concord_work_initiative", "concord_work_transition", "concord_work_relate", "concord_work_compact"])
type Pair = { tool: string; operation: string; queryId?: string }

const declaredPairs: Pair[] = ((envelopeSchema as any).$defs.toolOperation.oneOf as any[]).flatMap((branch) => {
  const properties = branch.properties
  const operations: string[] = "const" in properties.operation ? [properties.operation.const] : properties.operation.enum
  const queryId: string | undefined = properties.query_id?.const
  return operations.map((operation) => ({ tool: properties.tool.const as string, operation, queryId }))
})

function coreEnvelope({ tool, operation, queryId }: Pair) {
  const envelope: Record<string, unknown> = {
    schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core",
    tool, operation, ...(queryId ? { query_id: queryId } : {}), outcome: "ok", resolved_scope: null,
    authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [],
    next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, result: {},
  }
  if (writeTools.has(tool)) { envelope.changed_refs = []; envelope.next_valid_intents = [] }
  return envelope
}

// Issue #352: `$defs/base` conjoins `$defs/toolOperation` with its own
// `operation` enum and `query_id` pattern, so a pair the enum omits or the
// pattern rejects is unsatisfiable and every core response for it becomes an
// adapter `malformed_response`.
test("every declared tool/operation pair is satisfiable at the adapter boundary", () => {
  expect(declaredPairs.length).toBeGreaterThan(0)
  const refused = declaredPairs.filter((pair) => !validateGeneratedEnvelope(coreEnvelope(pair)))
  expect(refused.map((pair) => `${pair.tool}.${pair.operation}`)).toEqual([])
})

test("the four operations issue #352 reported, and their controls, all validate", () => {
  // The controls were never broken. They fail here only if the validator has
  // become permissive rather than correct, which is the way a green run of the
  // repaired operations above could otherwise lie.
  const repaired = ["concord_work_trace.continuity", "concord_work_trace.research", "concord_work_browse.resource_claims", "concord_work_browse.messages"]
  const controls = ["concord_work_trace.history", "concord_work_browse.scope"]
  for (const name of [...repaired, ...controls]) {
    const pair = declaredPairs.find((item) => `${item.tool}.${item.operation}` === name)
    expect(pair, `${name} is not declared in $defs/toolOperation`).toBeDefined()
    expect(validateGeneratedEnvelope(coreEnvelope(pair!)), name).toBe(true)
  }
})

test("a declared pair carrying another pair's query_id is still refused", () => {
  const continuity = declaredPairs.find((item) => item.operation === "continuity")!
  expect(validateGeneratedEnvelope(coreEnvelope({ ...continuity, queryId: "PM1.Q7" }))).toBe(false)
  expect(validateGeneratedEnvelope(coreEnvelope({ ...continuity, queryId: undefined }))).toBe(false)
})

test("an undeclared operation is refused even though the tool is declared", () => {
  expect(validateGeneratedEnvelope(coreEnvelope({ tool: "concord_work_trace", operation: "forecast", queryId: "PM1.Q7" }))).toBe(false)
})

test("every declared pair is nameable as a next valid intent", () => {
  // next_valid_intents carries its own copy of the operation enum and query_id
  // pattern under the same $defs/toolOperation conjunction, so it drifts the
  // same way and would otherwise make a repaired operation unreachable as a
  // follow-up even once the response itself validates.
  const refused = declaredPairs.filter((pair) => !validateGeneratedEnvelope({
    ...coreEnvelope({ tool: "concord_work_transition", operation: "lifecycle" }),
    next_valid_intents: [{ tool: pair.tool, operation: pair.operation, ...(pair.queryId ? { query_id: pair.queryId } : {}), reason_code: "inspect" }],
  }))
  expect(refused.map((pair) => `${pair.tool}.${pair.operation}`)).toEqual([])
})
