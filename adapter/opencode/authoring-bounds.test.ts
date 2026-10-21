import { expect, test } from "bun:test"
import { contractOperations, hostToolSchemas, payloadSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema, validateGeneratedPayload } from "./generated-contract-tests"
import { publishCiWatchDefinition } from "./ci-watch"
import { publishWorkerReadDefinition } from "./worker_source"
import { publishWorkerCheckDefinition } from "./worker_control"
import { publishWorkerDiffDefinition } from "./worker_diff"
import { publishRequestDefinition, publishWorkStartDefinition } from "./concord"
import { validateAgainstSchema } from "./dispatch"

// Bound-annotation contract for every enforced countable bound. The node
// enforcing a bound teaches it in its own description, one clause per bound
// family, rendered by the shared Go annotation owner:
//   Bounds: <floor>..<ceiling> <unit> inclusive   both endpoints declared
//   Bounds: >=<floor> <unit> inclusive            floor only, zero included
//   Bounds: <=<ceiling> <unit> inclusive          ceiling only
// <unit> is value, code points, UTF-8 bytes, items, or properties; clauses
// are separated by "; ". Every enforced minimum is stated, zero included: a
// declared 0 floor renders "0..N" or ">=0" by role. Only explicit "Bounds:"
// runs parse; prose and example numbers never satisfy a check. Three layers
// hold: canonical teaching (exact clauses on every reachable bound use),
// published inventory (the bound set of every hook output equals the
// canonical set per branch identity, through references, factored unions,
// and conditionals), and published teaching (each published bound node's
// description states the union of the canonical descriptions applicable at
// its position).

type Schema = Record<string, any>
type SchemaTable = Record<string, Schema>

// floor keyword, ceiling keyword, count unit.
const boundFamilies: [string, string, string][] = [
  ["minimum", "maximum", "value"],
  ["minLength", "maxLength", "code points"],
  ["x-minBytes", "x-maxBytes", "UTF-8 bytes"],
  ["minItems", "maxItems", "items"],
  ["minProperties", "maxProperties", "properties"],
]
const boundKeywords = boundFamilies.flatMap(([floorKeyword, ceilingKeyword]) => [floorKeyword, ceilingKeyword])
const unitOfKeyword: Record<string, string> = Object.fromEntries(
  boundFamilies.flatMap(([floorKeyword, ceilingKeyword, unit]) => [[floorKeyword, unit], [ceilingKeyword, unit]] as [string, string][]),
)

// floor is null only when the schema declares no floor; a declared 0 floor
// stays 0 and renders in the pair or >= role.
type BoundClause = { floor: number | null; ceiling: number | null; unit: string }

const clauseGrammar = /^(?:Bounds:\s*)?(>=|<=)?(-?\d+)(?:\.\.(-?\d+))?\s(value|code points|UTF-8 bytes|items|properties)\s+inclusive$/

function renderClause(clause: BoundClause): string {
  const range =
    clause.floor !== null && clause.ceiling !== null ? `${clause.floor}..${clause.ceiling}`
    : clause.floor !== null ? `>=${clause.floor}`
    : `<=${clause.ceiling}`
  return `Bounds: ${range} ${clause.unit} inclusive`
}

// parseBoundClauses reads "Bounds:" runs: one prefix, then grammar-matching
// segments across "; " until a segment fails; re-prefixed clauses inside a
// run parse once.
function parseBoundClauses(description: unknown): BoundClause[] {
  if (typeof description !== "string") return []
  const clauses: BoundClause[] = []
  let searchFrom = 0
  for (;;) {
    const start = description.indexOf("Bounds:", searchFrom)
    if (start < 0) break
    searchFrom = Math.max(parseBoundRun(description, start, clauses), start + 1)
  }
  return clauses
}

function parseBoundRun(description: string, start: number, clauses: BoundClause[]): number {
  let cursor = start + "Bounds:".length
  while (cursor < description.length) {
    while (cursor < description.length && /\s/.test(description[cursor])) cursor++
    if (description.startsWith("Bounds:", cursor)) {
      cursor += "Bounds:".length
      continue
    }
    const separator = description.indexOf(";", cursor)
    const match = clauseGrammar.exec(description.slice(cursor, separator < 0 ? description.length : separator).trim())
    if (!match) return cursor
    const [, role, first, second, unit] = match
    if (role !== undefined && second !== undefined) return cursor // ">=1..5" is no role form
    let floor: number | null = null
    let ceiling: number | null = null
    if (role === ">=") floor = Number(first)
    else if (role === "<=") ceiling = Number(first)
    else if (second !== undefined) [floor, ceiling] = [Number(first), Number(second)]
    else return cursor
    clauses.push({ floor, ceiling, unit })
    if (separator < 0) return description.length
    cursor = separator + 1
  }
  return cursor
}

// expectedClauses derives the clauses a node owes from the bounds it
// enforces, with the exact role per family.
function expectedClauses(node: Schema): BoundClause[] {
  const clauses: BoundClause[] = []
  for (const [floorKeyword, ceilingKeyword, unit] of boundFamilies) {
    const floor = typeof node[floorKeyword] === "number" ? (node[floorKeyword] as number) : null
    const ceiling = typeof node[ceilingKeyword] === "number" ? (node[ceilingKeyword] as number) : null
    if (floor !== null && ceiling !== null) clauses.push({ floor, ceiling, unit })
    else if (floor !== null) clauses.push({ floor, ceiling: null, unit })
    else if (ceiling !== null) clauses.push({ floor: null, ceiling, unit })
  }
  return clauses
}

// boundTeachingFindings compares stated with owed clauses in both
// directions: a missing clause under-teaches an enforced bound, a clause
// without the keyword teaches one no longer enforced.
function boundTeachingFindings(node: Schema, at: string): string[] {
  const owed = expectedClauses(node).map(renderClause).sort()
  const stated = parseBoundClauses(node.description).map(renderClause).sort()
  if (JSON.stringify(owed) === JSON.stringify(stated)) return []
  return [`${at}: enforced bounds teach [${owed.join("; ")}] but the description states [${stated.join("; ")}]`]
}

function clauseTextsOf(node: Schema): string[] {
  return parseBoundClauses(node.description).map(renderClause).sort()
}

function clauseTextSet(node: Schema): Set<string> {
  return new Set(parseBoundClauses(node.description).map(renderClause))
}

type BoundUse = { chain: string; at: string; node: Schema }

// conjoinUnion folds a oneOf parent's object constraints into each branch —
// the effective view: shared properties overlay, closure, type, and the
// required union apply to every branch. Mirrors the expanded published
// view's per-branch conjunction; merging twice is content-idempotent.
function conjoinUnion(parent: Schema, branch: Schema): Schema {
  const merged: Schema = { ...branch }
  if (parent.type !== undefined && merged.type === undefined) merged.type = parent.type
  if (parent.additionalProperties !== undefined && merged.additionalProperties === undefined) merged.additionalProperties = parent.additionalProperties
  const required = [...new Set([...(Array.isArray(parent.required) ? parent.required : []), ...(Array.isArray(merged.required) ? merged.required : [])])]
  if (required.length > 0) merged.required = required
  const parentProperties = parent.properties !== null && typeof parent.properties === "object" && !Array.isArray(parent.properties) ? parent.properties : {}
  const childProperties = merged.properties !== null && typeof merged.properties === "object" && !Array.isArray(merged.properties) ? merged.properties : {}
  const properties: Record<string, Schema> = { ...parentProperties }
  for (const [name, child] of Object.entries(childProperties)) {
    const parentProperty = parentProperties[name]
    properties[name] =
      parentProperty !== null && typeof parentProperty === "object" && !Array.isArray(parentProperty) && child !== null && typeof child === "object" && !Array.isArray(child)
        ? { ...parentProperty, ...child }
        : child
  }
  if (Object.keys(properties).length > 0) merged.properties = properties
  return merged
}

// boundInventory walks schemas the way the validator reads them: local
// "#/$defs/..." references (cycle-safe), conditional and forbidden frames,
// composition branches, properties, patterns, items, additional properties,
// propertyNames, contains. Chains carry property names, const anchors,
// patterns (~pat), items ([]), additionalProperties (<>), names()/has(),
// conditional frame tokens (if:/then:/else:/not:), and indexed branch
// tokens (oneOf[i] and siblings): sibling branches keep distinct identity,
// and a oneOf folds into its conjoined branches, so a factored union's
// expanded view walks to the same chains as the authored per-branch form.
// Returns the "chain#keyword=value(unit)" signature set, the bound
// positions, the described nodes per chain, and unresolvable references.
function boundInventory(roots: { at: string; schema: unknown }[], table: SchemaTable): { uses: Set<string>; bounded: BoundUse[]; described: Map<string, Schema[]>; unresolved: string[] } {
  const uses = new Set<string>()
  const bounded = new Map<string, BoundUse>()
  const described = new Map<string, Schema[]>()
  const unresolved: string[] = []
  const walk = (node: unknown, chain: string, stack: string[]): void => {
    if (node === null || typeof node !== "object" || Array.isArray(node)) return
    const schema = node as Schema
    for (const keyword of boundKeywords) {
      if (typeof schema[keyword] !== "number") continue
      uses.add(`${chain}#${keyword}=${schema[keyword]}(${unitOfKeyword[keyword]})`)
      const position = `${chain}#${keyword}`
      if (!bounded.has(position)) bounded.set(position, { chain, at: position, node: schema })
      const nodes = described.get(chain) ?? []
      if (!nodes.includes(schema)) nodes.push(schema)
      described.set(chain, nodes)
    }
    if (typeof schema.$ref === "string") {
      const name = schema.$ref.split("/").at(-1) as string
      const target = table[name]
      if (target === undefined) {
        unresolved.push(`${chain}: reference ${schema.$ref} resolves nowhere`)
        return
      }
      if (!stack.includes(name)) walk(target, chain, [...stack, name])
    }
    for (const key of ["if", "then", "else", "not"]) walk(schema[key], `${chain}.${key}:`, stack)
    for (const key of ["allOf", "anyOf", "prefixItems"]) {
      (schema[key] as unknown[] | undefined)?.forEach((branch, index) => walk(branch, `${chain}.${key}[${index}]`, stack))
    }
    for (const [pattern, child] of Object.entries(schema.patternProperties ?? {})) walk(child, `${chain}~${pattern}`, stack)
    if (schema.propertyNames !== null && typeof schema.propertyNames === "object") walk(schema.propertyNames, `${chain}names()`, stack)
    if (schema.contains !== null && typeof schema.contains === "object") walk(schema.contains, `${chain}has()`, stack)
    const items = schema.items
    if (Array.isArray(items)) items.forEach(item => walk(item, `${chain}[]`, stack))
    else walk(items, `${chain}[]`, stack)
    const unions = schema.oneOf as unknown[] | undefined
    if (unions === undefined) {
      for (const [name, child] of Object.entries(schema.properties ?? {})) {
        const declaresConst = child !== null && typeof child === "object" && !Array.isArray(child) && (child as Schema).const !== undefined
        walk(child, `${chain}.${declaresConst ? `${name}=${JSON.stringify((child as Schema).const)}` : name}`, stack)
      }
      if (schema.additionalProperties !== null && typeof schema.additionalProperties === "object" && !Array.isArray(schema.additionalProperties)) {
        walk(schema.additionalProperties, `${chain}<>`, stack)
      }
      return
    }
    // A oneOf applies its own object constraints to every branch, so the
    // effective positions are the conjoined branches; a factored union's
    // lifted parent properties reach the same chains instead of separate
    // parent-only ones.
    unions.forEach((branch, index) => {
      const effective = branch !== null && typeof branch === "object" && !Array.isArray(branch) ? conjoinUnion(schema, branch as Schema) : branch
      walk(effective, `${chain}.oneOf[${index}]`, stack)
    })
  }
  for (const root of roots) walk(root.schema, root.at, [])
  return { uses, bounded: [...bounded.values()], described, unresolved }
}

// canonicalBoundRoots assembles the served contract: one input schema per
// operation, one schema per public workflow_action variant, and the host
// tool schemas with concord_work_start split into its capture and resume
// modes (its published conditional carries those two branches) plus their
// merged root-property view.
function canonicalBoundRoots(): { at: string; schema: unknown }[] {
  const payloadTable = payloadSchemas as unknown as SchemaTable
  const roots: { at: string; schema: unknown }[] = []
  for (const operation of contractOperations as unknown as { id: string; tool: string; input_schema: string }[]) {
    if (operation.id === "concord_work_transition.workflow_action") continue
    const name = operation.input_schema.replace(/^#\/(\$defs|schemas)\//, "")
    roots.push({ at: `@${operation.tool}.${operation.id.slice(operation.id.indexOf(".") + 1)}`, schema: payloadTable[name] })
  }
  for (const variant of workflowActionPublicVariants) {
    roots.push({ at: `@concord_work_transition.workflow_action#${variant.action_id}`, schema: payloadTable[variant.schema] })
  }
  const [capture, resume] = (hostToolSchemas as unknown as SchemaTable).concord_work_start.oneOf as [Schema, Schema]
  roots.push(
    { at: "@concord_work_start#capture", schema: capture },
    { at: "@concord_work_start#resume", schema: resume },
    { at: "@concord_work_start", schema: { type: "object", additionalProperties: false, properties: { ...capture.properties, ...resume.properties } } },
  )
  for (const [tool, schema] of Object.entries(hostToolSchemas)) {
    if (tool !== "concord_work_start") roots.push({ at: `@${tool}`, schema })
  }
  return roots
}

// publishedBoundRoots collects every hook output restored to its
// legacy-effective view: one expanded branch per operation (one per
// workflow_action variant), concord_work_start split into else (capture),
// then (resume), and its merged root properties, and the other hooks whole.
async function publishedBoundRoots(): Promise<{ at: string; schema: unknown }[]> {
  const roots: { at: string; schema: unknown }[] = []
  for (const toolID of new Set(contractOperations.map(operation => (operation as unknown as { tool: string }).tool))) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await publishRequestDefinition({ toolID }, output)
    const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
    for (const branch of expanded.oneOf) {
      const operationConst = branch.properties.operation.const
      roots.push(
        operationConst === "workflow_action"
          ? { at: `@concord_work_transition.workflow_action#${branch.properties.input.properties.action_id.const}`, schema: branch.properties.input }
          : { at: `@${toolID}.${operationConst}`, schema: branch.properties.input },
      )
    }
  }
  for (const [toolID, hook] of [
    ["concord_work_start", publishWorkStartDefinition],
    ["concord_ci_watch", publishCiWatchDefinition],
    ["concord_worker_read", publishWorkerReadDefinition],
    ["concord_worker_check", publishWorkerCheckDefinition],
    ["concord_worker_diff", publishWorkerDiffDefinition],
  ] as const) {
    const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
    await hook({ toolID }, output)
    const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
    if (toolID === "concord_work_start") {
      roots.push(
        { at: "@concord_work_start#capture", schema: expanded.else },
        { at: "@concord_work_start#resume", schema: expanded.then },
        { at: "@concord_work_start", schema: { type: "object", additionalProperties: false, properties: expanded.properties } },
      )
      continue
    }
    roots.push({ at: `@${toolID}`, schema: expanded })
  }
  return roots
}

test("bounds checker: the clause grammar rejects wrong roles, values, units, missing inclusive wording, and stale clauses", () => {
  const check = (node: Schema) => boundTeachingFindings(node, "@witness")
  expect(check({ type: "string", minLength: 1, maxLength: 512, description: `Accepted shape: string Bounds: 1..512 code points inclusive; example: "a"` })).toEqual([])
  // Multiple families: a repeated prefix and a bare "; " continuation both parse, once each.
  const control = { type: "string", minLength: 1, maxLength: 128, "x-minBytes": 2, "x-maxBytes": 128 }
  expect(check({ ...control, description: "Bounds: 1..128 code points inclusive; Bounds: 2..128 UTF-8 bytes inclusive" })).toEqual([])
  expect(check({ ...control, description: "Bounds: 1..128 code points inclusive; 2..128 UTF-8 bytes inclusive" })).toEqual([])
  // Rejections: wrong unit, drifted value, exclusive wording, swapped roles,
  // reversed endpoints, role-range mixture, and loose tokens without the grammar.
  for (const node of [
    { maxLength: 512, description: "Bounds: <=512 UTF-8 bytes inclusive" },
    { minLength: 1, maxLength: 512, description: "Bounds: 1..511 code points inclusive" },
    { maxLength: 512, description: "Bounds: <=512 code points" },
    { minimum: 1, description: "Bounds: <=1 value inclusive" },
    { minimum: 1, maximum: 100000, description: "Bounds: 100000..1 value inclusive" },
    { minLength: 1, maxLength: 5, description: "Bounds: >=1..5 code points inclusive" },
    { maxLength: 512, description: "Up to 512 code points, inclusive." },
    { type: "string", description: "Bounds: <=64 code points inclusive" },
  ]) {
    expect(check(node).length).toBeGreaterThan(0)
  }
  // Prose and example numbers around a correct clause never mislead it.
  expect(check({ type: "string", minLength: 1, maxLength: 512, description: `budget 512 bytes, 1 example, revision 3. Bounds: 1..512 code points inclusive; example: "v512"` })).toEqual([])
  // Every enforced minimum is stated, zero included, in its exact role.
  expect(check({ minLength: 0, maxLength: 64, description: "Bounds: 0..64 code points inclusive" })).toEqual([])
  expect(check({ minLength: 0, maxLength: 64, description: "Bounds: <=64 code points inclusive" }).length).toBeGreaterThan(0)
  expect(check({ minimum: 0, description: "Bounds: >=0 value inclusive" })).toEqual([])
  expect(check({ minimum: 0, description: "value between 0 and unbounded, inclusive" }).length).toBeGreaterThan(0)
  expect(check({ minLength: 0, description: "Bounds: >=0 code points inclusive" })).toEqual([])
  expect(check({ minimum: -100, maximum: 100, description: "Bounds: -100..100 value inclusive" })).toEqual([])
  expect(check({ minimum: -100, maximum: 100, description: "Bounds: <=100 value inclusive" }).length).toBeGreaterThan(0)
})

test("bounds checker: the inventory follows references, keeps branch identity, and survives factoring", () => {
  const usesOf = (schema: unknown, table: SchemaTable = {}) => boundInventory([{ at: "@w", schema }], table).uses
  const teachingDefs: SchemaTable = {
    witness_outer: { type: "object", additionalProperties: false, properties: { middle: { $ref: "#/$defs/witness_middle" } }, required: ["middle"] },
    witness_middle: { type: "object", properties: { inner: { minLength: 2, $ref: "#/$defs/witness_inner" } }, required: ["inner"] },
    witness_inner: { type: "string", maxLength: 9 },
    witness_other: { type: "string" },
  }
  const chained = boundInventory([{ at: "@w", schema: teachingDefs.witness_outer }], teachingDefs)
  expect(chained.unresolved).toEqual([])
  expect(chained.uses.has("@w.middle.inner#maxLength=9(code points)")).toBe(true)
  expect(chained.uses.has("@w.middle.inner#minLength=2(code points)")).toBe(true)
  // A replaced reference loses the bound use; a broken one is an explicit finding.
  const replaced = JSON.parse(JSON.stringify(teachingDefs))
  replaced.witness_middle.properties.inner.$ref = "#/$defs/witness_other"
  expect(usesOf(replaced.witness_outer, replaced).has("@w.middle.inner#maxLength=9(code points)")).toBe(false)
  const broken = JSON.parse(JSON.stringify(teachingDefs))
  broken.witness_middle.properties.inner.$ref = "#/$defs/witness_gone"
  expect(boundInventory([{ at: "@w", schema: broken.witness_outer }], broken).unresolved.some(finding => finding.includes("#/$defs/witness_gone"))).toBe(true)
  const siblingTable: SchemaTable = { base: { type: "object", properties: { inherited: { maxLength: 9 } } } }
  const siblings = {
    $ref: "#/$defs/base",
    properties: { own: { maxLength: 4 } },
    allOf: [{ properties: { composed: { minLength: 2 } } }],
  }
  const siblingUses = usesOf(siblings, siblingTable)
  expect([...siblingUses].sort()).toEqual([
    "@w.allOf[0].composed#minLength=2(code points)",
    "@w.inherited#maxLength=9(code points)",
    "@w.own#maxLength=4(code points)",
  ])
  const droppedSibling = JSON.parse(JSON.stringify(siblings))
  delete droppedSibling.allOf[0].properties.composed.minLength
  expect([...siblingUses].filter(use => !usesOf(droppedSibling, siblingTable).has(use))).toEqual([
    "@w.allOf[0].composed#minLength=2(code points)",
  ])
  // Branch identity: two branches bounding the same field differently stay
  // distinct, so removing either side is detected.
  const branchy = { oneOf: [{ properties: { value: { minLength: 2, maxLength: 4 } } }, { properties: { value: { minLength: 5, maxLength: 8 } } }] }
  const branchyUses = usesOf(branchy)
  expect(branchyUses.has("@w.oneOf[0].value#maxLength=4(code points)")).toBe(true)
  expect(branchyUses.has("@w.oneOf[1].value#maxLength=8(code points)")).toBe(true)
  const lostSecond = JSON.parse(JSON.stringify(branchy))
  delete lostSecond.oneOf[1].properties.value.maxLength
  expect(usesOf(lostSecond).has("@w.oneOf[1].value#maxLength=8(code points)")).toBe(false)
  // Equal bounds on sibling branches are also distinguished: dropping one
  // side cannot hide behind the other's identical signature.
  const twin = { oneOf: [{ properties: { value: { maxLength: 8 } } }, { properties: { value: { maxLength: 8 } } }] }
  expect(usesOf(twin).has("@w.oneOf[0].value#maxLength=8(code points)")).toBe(true)
  const lostTwin = JSON.parse(JSON.stringify(twin))
  delete lostTwin.oneOf[1].properties.value.maxLength
  expect(usesOf(lostTwin).has("@w.oneOf[1].value#maxLength=8(code points)")).toBe(false)
  // Conditionals keep then/else identity.
  const conditional = {
    if: { properties: { kind: { const: "short" } } },
    then: { properties: { value: { minLength: 2 } } },
    else: { properties: { value: { minLength: 5, maxLength: 8 } } },
  }
  const conditionalUses = usesOf(conditional)
  expect(conditionalUses.has("@w.then:.value#minLength=2(code points)")).toBe(true)
  expect(conditionalUses.has("@w.else:.value#maxLength=8(code points)")).toBe(true)
  const lostBranchBound = JSON.parse(JSON.stringify(conditional))
  delete lostBranchBound.else.properties.value.maxLength
  expect(usesOf(lostBranchBound).has("@w.else:.value#maxLength=8(code points)")).toBe(false)
  // Factoring: once a factored union's inherited constraints are restored
  // per branch (what the expanded published view carries), its inventory
  // equals the authored per-branch form.
  const shared = { maxLength: 6 }
  const unfactored = {
    type: "object",
    properties: { shared: { ...shared } },
    oneOf: [
      { type: "object", properties: { shared: { ...shared }, alpha: { minLength: 3 } }, required: ["shared", "alpha"] },
      { type: "object", properties: { shared: { ...shared }, beta: { maxItems: 4 } }, required: ["shared", "beta"] },
    ],
  }
  const expandedFactored = {
    type: "object",
    properties: { shared: { ...shared } },
    required: ["shared"],
    oneOf: [
      { properties: { shared: { ...shared }, alpha: { minLength: 3 } }, required: ["alpha"] },
      { properties: { shared: { ...shared }, beta: { maxItems: 4 } }, required: ["beta"] },
    ],
  }
  const expandedUses = usesOf(expandedFactored)
  expect(expandedUses).toEqual(usesOf(unfactored))
  expect(expandedUses.has("@w.oneOf[0].alpha#minLength=3(code points)")).toBe(true)
  expect(expandedUses.has("@w.oneOf[1].beta#maxItems=4(items)")).toBe(true)
  // A dropped keyword: the canonical side expects it, the mutated copy lost
  // it, and the set difference names it exactly.
  const enforced = { type: "object", additionalProperties: false, properties: { name: { type: "string", minLength: 1, maxLength: 64 } }, required: ["name"] }
  const dropped = JSON.parse(JSON.stringify(enforced))
  delete dropped.properties.name.maxLength
  expect([...usesOf(enforced)].filter(use => !usesOf(dropped).has(use))).toEqual(["@w.name#maxLength=64(code points)"])
})

test("capture keeps its closed surface and source-free research inputs validate", () => {
  const [capture] = hostToolSchemas.concord_work_start.oneOf
  const research = { title: "Research", value_statement: "Findings without source changes", kind: "research", task: "Investigate only", idempotency_key: "research" }
  expect(validateAgainstSchema(capture, research, [])).toBe(true)
  const input = { ...research, project_ids: ["project-1"] }
  expect(validateGeneratedPayload("work_define_capture_input", input)).toBe(true)
  const size_inputs = { behavior: "investigate", security: false, user_visible: false, blast_radius: "contained" }
  expect(validateAgainstSchema(capture, { ...research, size_inputs }, [])).toBe(false)
  expect(validateGeneratedPayload("work_define_capture_input", { ...input, size_inputs })).toBe(false)
})

test("observation publication retains code-point bounds after reference expansion", async () => {
  const output = { description: "", parameters: {}, jsonSchema: undefined as unknown }
  await publishRequestDefinition({ toolID: "concord_work_define" }, output)
  const expanded: any = expandedPublishedRequestSchema(output.jsonSchema)
  const input = expanded.oneOf.find((branch: any) => branch.properties.operation.const === "observation_record").properties.input
  for (const [name, maximum] of [["statement", 512], ["refs", 256], ["tags", 32]] as const) {
    const field = name === "statement" ? input.properties[name] : input.properties[name].items
    expect([field.minLength, field.maxLength]).toEqual([1, maximum])
    expect(field.description).toContain("code points")
    expect(field.description).toContain("inclusive")
    expect(field["x-maxBytes"]).toBeUndefined()
  }
  for (const [name, maximum] of [["refs", 16], ["tags", 8]] as const) {
    expect(input.properties[name].maxItems).toBe(maximum)
  }
})

test("every reachable enforced bound teaches its exact Bounds clause with role, value, unit and inclusive endpoints", () => {
  const payloadTable = payloadSchemas as unknown as SchemaTable
  const canonical = boundInventory(canonicalBoundRoots(), payloadTable)
  expect(canonical.unresolved).toEqual([])
  expect(canonical.uses.size).toBeGreaterThan(1000)
  const findings = new Set<string>()
  for (const use of canonical.bounded) {
    for (const finding of boundTeachingFindings(use.node, use.at)) findings.add(finding)
  }
  // Pinned teaching examples: exact role, value, unit, and inclusive endpoints.
  expect(clauseTextsOf(payloadTable.work_define_observation_record_input.properties.statement)).toEqual(["Bounds: 1..512 code points inclusive"])
  expect(clauseTextsOf((hostToolSchemas as unknown as SchemaTable).concord_worker_check.properties.control_id)).toEqual(
    ["Bounds: 1..128 code points inclusive", "Bounds: 2..128 UTF-8 bytes inclusive"].sort(),
  )
  expect([...findings]).toEqual([])
})

test("published tool schemas keep the canonical bound inventory through references, factored branches and conditionals", async () => {
  const payloadTable = payloadSchemas as unknown as SchemaTable
  const canonical = boundInventory(canonicalBoundRoots(), payloadTable)
  const published = boundInventory(await publishedBoundRoots(), payloadTable)
  expect(published.unresolved).toEqual([])
  expect(published.uses.size).toBeGreaterThan(1000)
  expect([...canonical.uses].filter(use => !published.uses.has(use))).toEqual([])
  expect([...published.uses].filter(use => !canonical.uses.has(use))).toEqual([])
})

test("every published bound node teaches the union of the canonical clauses applicable at its position", async () => {
  const payloadTable = payloadSchemas as unknown as SchemaTable
  const canonical = boundInventory(canonicalBoundRoots(), payloadTable)
  const applicable = new Map<string, Set<string>>()
  for (const use of canonical.bounded) {
    const union = applicable.get(use.chain) ?? new Set<string>()
    for (const text of clauseTextSet(use.node)) union.add(text)
    applicable.set(use.chain, union)
  }
  const published = boundInventory(await publishedBoundRoots(), payloadTable)
  const findings = new Set<string>()
  for (const [chain, nodes] of published.described) {
    const owed = applicable.get(chain)
    if (owed === undefined) continue
    for (const node of nodes) {
      const stated = clauseTextSet(node)
      if (owed.size === stated.size && [...owed].every(text => stated.has(text))) continue
      findings.add(`${chain}: published description must teach [${[...owed].sort().join("; ")}] but states [${[...stated].sort().join("; ")}]`)
    }
  }
  expect([...findings]).toEqual([])
})
