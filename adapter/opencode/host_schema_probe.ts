import { contractOperations, hostToolSchemas } from "./generated-contracts"
import { domain, knowledge, product_view, publishWorkStartDefinition, work_browse, work_compact, work_define, work_initiative, work_relate, work_start, work_trace, work_transition } from "./concord"

const tools: Record<string, any> = {
  concord_product_view: product_view,
  concord_work_browse: work_browse,
  concord_work_trace: work_trace,
  concord_knowledge: knowledge,
  concord_work_define: work_define,
  concord_domain: domain,
  concord_work_initiative: work_initiative,
  concord_work_transition: work_transition,
  concord_work_relate: work_relate,
  concord_work_compact: work_compact,
}

// Apply the production hook to the host's per-field schema. The expected
// contract is used for comparison only, never to repair the observed schema.
const expectedWorkStart = object(hostToolSchemas.concord_work_start, "generated work start schema")
const expectedWorkStartBranches = (expectedWorkStart.oneOf as any[]).filter((branch) => object(branch, "generated work start branch"))
if (expectedWorkStartBranches.length !== 2) fail("generated work start schema must carry the capture and resume branches")
const expectedWorkStartProperties = Object.assign({}, ...expectedWorkStartBranches.map((branch) => object(branch.properties, "generated work start branch properties")))
const workStartDefinition = {
  description: work_start.description,
  parameters: {},
  jsonSchema: publishedArgsSchema(work_start.args, "work start schema"),
}
await publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
const workStartRoot = object(workStartDefinition.jsonSchema, "published work start schema")
const workStartProperties = object(workStartRoot.properties, "work start properties")
if (JSON.stringify(Object.keys(workStartProperties).sort()) !== JSON.stringify(Object.keys(expectedWorkStartProperties).sort())) {
  fail("concord_work_start does not publish the generated argument set")
}
if (workStartRoot.required.length !== 0) fail("concord_work_start published view must keep every argument optional")
for (const [name, expected] of Object.entries(expectedWorkStartProperties)) {
  const actual = object(workStartProperties[name], `published work start property ${name}`)
  for (const [keyword, value] of Object.entries(object(expected, `generated work start property ${name}`))) {
    if (JSON.stringify(actual[keyword]) !== JSON.stringify(value)) fail(`concord_work_start ${name}.${keyword} differs from the generated contract`)
  }
}
inspect(workStartRoot, workStartRoot)

function fail(message: string): never {
  throw new Error(message)
}

function object(value: unknown, label: string): Record<string, any> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail(`${label} is not an object`)
  return value as Record<string, any>
}

function inspect(value: unknown, path = "$", seen = new Set<unknown>()): void {
  if (typeof value !== "object" || value === null || seen.has(value)) return
  seen.add(value)
  if (Array.isArray(value)) {
    value.forEach((item, index) => inspect(item, `${path}[${index}]`, seen))
    return
  }
  for (const [key, item] of Object.entries(value)) {
    if (key === "~standard" || key === "def") fail(`published schema contains Zod implementation key ${path}.${key}`)
    if (key === "$ref" || key === "oneOf" || key === "anyOf" || key === "allOf" || key === "definitions") fail(`published schema contains host-unsafe ${key} at ${path}`)
    inspect(item, `${path}.${key}`, seen)
  }
}

for (const [toolName, exportedTool] of Object.entries(tools)) {
  const root = publishedArgsSchema(exportedTool.args, `${toolName} schema`)
  inspect(root)
  if (root.type !== "object") fail(`${toolName} schema root is not an object`)
  if (JSON.stringify(root.required) !== JSON.stringify(["request"])) fail(`${toolName} schema does not require only request`)
  const properties = object(root.properties, `${toolName} properties`)
  if (JSON.stringify(Object.keys(properties)) !== JSON.stringify(["request"])) fail(`${toolName} schema exposes fields outside request`)
  const request = object(properties.request, `${toolName} request`)
  const expected = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (JSON.stringify(request.required) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request fields are not required`)
  const operation = object(request.properties.operation, `${toolName} operation`)
  if (JSON.stringify(operation.enum) !== JSON.stringify(expected.map((candidate: any) => candidate.id.slice(candidate.id.indexOf(".") + 1)))) fail(`${toolName} operation enum differs from the generated contract`)
  const input = object(request.properties.input, `${toolName} input`)
  if (input.type !== "object" || input.additionalProperties !== true || input.required.length !== 0) fail(`${toolName} input is not a permissive object`)
}

const workDefineRoot = publishedArgsSchema(work_define.args, "work define schema")
const workDefineRequest = object(object(workDefineRoot.properties, "work define properties").request, "work define request")
const urgencyProperty = object(object(object(workDefineRequest.properties, "work define request properties").input, "work define input").properties, "work define input properties").urgency
const urgency = object(urgencyProperty, "capture urgency")
if (JSON.stringify(urgency.enum) !== JSON.stringify(["standard", "expedite"])) fail("capture urgency enum is not published")

function publishedArgsSchema(args: Record<string, unknown>, label: string, required = Object.keys(args)): Record<string, any> {
  const properties = Object.fromEntries(Object.entries(args).filter(([, value]) => value !== undefined))
  return object({ type: "object", properties, required }, label)
}

console.log(`host schema probe passed (${Object.keys(tools).length + 1} tools, ${contractOperations.length} operations)`)
