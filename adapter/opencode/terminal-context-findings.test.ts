import { test, expect } from "bun:test"
import { readFileSync } from "node:fs"
import { contextFindingsAggregateRefusal, validateAgainstSchema } from "./dispatch"

// CON-887 terminal-report content foundation. The report contract file is the
// owned source of truth for the optional context_findings member, so this
// suite reads contracts/agent-lane-report.schema.json from disk and validates
// against it directly: the bounds prove out the moment the contract changes,
// before the generated schema embed catches up. The embedded-schema admission
// and terminal-payload coverage lives in dispatch.test.ts.

const schema = JSON.parse(readFileSync(new URL("../../contracts/agent-lane-report.schema.json", import.meta.url), "utf8")) as {
  properties: Record<string, any>
  required: string[]
  additionalProperties: boolean
  $defs: Record<string, any>
}

const kinds = ["observation", "inference", "hypothesis", "rejected_approach", "open_question", "contradiction", "direction"] as const

const finding = (overrides: Record<string, unknown> = {}) => ({
  kind: "observation",
  statement: "the bounded read is the only admission route",
  subject_ref: "adapter/opencode/dispatch.ts",
  evidence_refs: ["contracts/agent-lane-report.schema.json"],
  ...overrides,
})

const report = (overrides: Record<string, unknown> = {}) => ({
  schema_version: "1.0",
  readback_model: "opencode/glm/concord-2",
  status: "completed",
  evidence: [{ obligation: "commands", detail: "bun test adapter/opencode/" }],
  ...overrides,
})

const admits = (value: unknown, label: string) => {
  const failures: string[] = []
  expect(validateAgainstSchema(schema, value, failures), `${label}: ${failures.join("; ")}`).toBe(true)
}

const refuses = (value: unknown, label: string) => {
  const failures: string[] = []
  expect(validateAgainstSchema(schema, value, failures), label).toBe(false)
  return failures.join("; ")
}

test("the contract declares context_findings as an optional bounded member without a new identity", () => {
  const property = schema.properties.context_findings
  expect(property).toBeDefined()
  expect(property.type).toBe("array")
  expect(property.minItems).toBe(0)
  expect(property.maxItems).toBe(16)
  expect(property["x-maxArrayBytes"]).toBe(16384)
  // Optional content on the unchanged identities: no new required top-level
  // field, and the report object stays closed.
  expect(schema.required).toEqual(["schema_version", "readback_model", "status", "evidence"])
  expect(schema.additionalProperties).toBe(false)
  expect(schema.properties.schema_version.enum).toEqual(["1.0", "1.1"])
  refuses(report({ context_findings: [finding()], unexpected: true }), "unknown sibling of context_findings")
})

test("the contract closes the context_finding entry on four required fields", () => {
  const entry = schema.$defs.context_finding
  expect(entry.type).toBe("object")
  expect(entry.additionalProperties).toBe(false)
  expect(entry.required).toEqual(["kind", "statement", "subject_ref", "evidence_refs"])
  expect(entry.properties.kind.enum).toEqual([...kinds])
  const statement = entry.properties.statement
  expect(statement.minLength).toBe(1)
  expect(statement.maxLength).toBe(1024)
  expect(statement["x-maxBytes"]).toBe(1024)
  const subjectRef = entry.properties.subject_ref
  expect(subjectRef.minLength).toBe(1)
  expect(subjectRef.maxLength).toBe(128)
  expect(subjectRef["x-maxBytes"]).toBe(128)
  const refs = entry.properties.evidence_refs
  expect(refs.minItems).toBe(0)
  expect(refs.maxItems).toBe(8)
  expect(refs.items.minLength).toBe(1)
  expect(refs.items.maxLength).toBe(256)
  expect(refs.items["x-maxBytes"]).toBe(256)
})

test("the contract admits absent, empty, every-kind, and at-bound findings on both identities", () => {
  admits(report(), "absent findings")
  admits(report({ context_findings: [] }), "empty findings")
  admits(report({ context_findings: kinds.map((kind) => finding({ kind })) }), "every kind")
  admits(report({ context_findings: Array.from({ length: 16 }, () => finding()) }), "sixteen entries")
  admits(report({ context_findings: [finding({ statement: "x".repeat(1024) })] }), "1024-byte ASCII statement")
  admits(report({ context_findings: [finding({ statement: "é".repeat(512) })] }), "1024-byte multi-byte statement")
  admits(report({ context_findings: [finding({ statement: "🎉".repeat(256) })] }), "1024-byte astral statement")
  admits(report({ context_findings: [finding({ subject_ref: "s".repeat(128) })] }), "128-byte subject_ref")
  admits(report({ context_findings: [finding({ evidence_refs: Array.from({ length: 8 }, () => "r".repeat(256)) })] }), "eight 256-byte refs")
  for (const schema_version of ["1.0", "1.1"] as const) {
    admits(report({ schema_version, context_findings: [finding()] }), `identity ${schema_version}`)
  }
})

test("the contract refuses over-bound and drifted findings", () => {
  const refusals = [
    { name: "seventeen entries", value: Array.from({ length: 17 }, () => finding()) },
    { name: "kind outside the enum", value: [finding({ kind: "vibes" })] },
    { name: "missing kind", value: [{ statement: "s", subject_ref: "r", evidence_refs: [] }] },
    { name: "missing statement", value: [{ kind: "observation", subject_ref: "r", evidence_refs: [] }] },
    { name: "missing subject_ref", value: [{ kind: "observation", statement: "s", evidence_refs: [] }] },
    { name: "missing evidence_refs", value: [{ kind: "observation", statement: "s", subject_ref: "r" }] },
    { name: "undeclared entry field", value: [finding({ acceptance: true })] },
    { name: "empty statement", value: [finding({ statement: "" })] },
    { name: "statement past 1024 code points", value: [finding({ statement: "x".repeat(1025) })] },
    { name: "statement past 1024 UTF-8 bytes within code points", value: [finding({ statement: "é".repeat(513) })] },
    { name: "statement past bytes with astral characters", value: [finding({ statement: "🎉".repeat(257) })] },
    { name: "subject_ref past 128 code points", value: [finding({ subject_ref: "s".repeat(129) })] },
    { name: "subject_ref past 128 UTF-8 bytes within code points", value: [finding({ subject_ref: "é".repeat(65) })] },
    { name: "nine evidence refs", value: [finding({ evidence_refs: Array.from({ length: 9 }, (_, index) => `ref:${index}`) })] },
    { name: "empty evidence ref", value: [finding({ evidence_refs: [""] })] },
    { name: "evidence ref past 256 code points", value: [finding({ evidence_refs: ["r".repeat(257)] })] },
    { name: "evidence ref past 256 UTF-8 bytes within code points", value: [finding({ evidence_refs: ["é".repeat(129)] })] },
  ]
  for (const refusal of refusals) {
    refuses(report({ context_findings: refusal.value }), refusal.name)
  }
  // A non-array and a non-object entry are not findings at all.
  refuses(report({ context_findings: "see prose above" }), "string findings")
  refuses(report({ context_findings: ["observation"] }), "string entry")
})

// The aggregate bound is enforced by the adapter (dispatch.ts
// contextFindingsAggregateRefusal) because the schema subset expresses no
// serialized-size keyword; the bound it enforces is the contract's
// x-maxArrayBytes, and the refusal never rewrites the findings.
test("the aggregate refusal bound counts the compact serialization and never truncates", () => {
  const bound = schema.properties.context_findings["x-maxArrayBytes"] as number
  const small = Array.from({ length: 16 }, (_, index) => finding({ statement: `short ${index}` }))
  expect(contextFindingsAggregateRefusal(small, bound)).toBeNull()
  expect(contextFindingsAggregateRefusal([], bound)).toBeNull()
  expect(contextFindingsAggregateRefusal(undefined, bound)).toBeNull()

  const oversized = Array.from({ length: 16 }, (_, index) => finding({ statement: `finding ${index}: `.padEnd(14, " ") + "x".repeat(1010) }))
  expect(Buffer.byteLength(JSON.stringify(oversized), "utf8")).toBeGreaterThan(bound)
  const frozen = structuredClone(oversized)
  const refusal = contextFindingsAggregateRefusal(oversized, bound)
  expect(refusal).toContain("context_findings")
  expect(refusal).toContain(String(bound))
  expect(oversized).toEqual(frozen)

  // Just under the bound stays admissible: the bound is exact, not conservative.
  const fitting = Array.from({ length: 12 }, (_, index) => finding({ statement: `finding ${index}: `.padEnd(14, " ") + "x".repeat(1010), subject_ref: "s".repeat(128) }))
  expect(Buffer.byteLength(JSON.stringify(fitting), "utf8")).toBeLessThanOrEqual(bound)
  expect(Buffer.byteLength(JSON.stringify(fitting), "utf8")).toBeGreaterThan(bound - 2000)
  expect(contextFindingsAggregateRefusal(fitting, bound)).toBeNull()
})
