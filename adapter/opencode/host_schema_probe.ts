import { tool } from "@opencode-ai/plugin"
import { contractOperations, hostToolSchemas, payloadSchemas } from "./generated-contracts"
import { domain, knowledge, product_view, work_browse, work_compact, work_define, work_initiative, work_relate, work_start, work_trace, work_transition } from "./concord"

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

const workStartRoot = object(tool.schema.toJSONSchema(tool.schema.object(work_start.args), { io: "input" }), "work start schema")
const workStartProperties = object(workStartRoot.properties, "work start properties")
const expectedWorkStart = object(hostToolSchemas.concord_work_start, "generated work start schema")
const expectedWorkStartProperties = object(expectedWorkStart.properties, "generated work start properties")
if (JSON.stringify(Object.keys(workStartProperties).sort()) !== JSON.stringify(Object.keys(expectedWorkStartProperties).sort())) {
  fail("concord_work_start does not publish the generated argument set")
}
if (JSON.stringify([...workStartRoot.required].sort()) !== JSON.stringify([...expectedWorkStart.required].sort())) fail("concord_work_start required arguments differ from the generated contract")
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

function resolvePointer(root: unknown, pointer: string): unknown {
  if (!pointer.startsWith("#/")) fail(`non-local published schema reference ${pointer}`)
  let value = root
  for (const token of pointer.slice(2).split("/")) {
    const key = token.replaceAll("~1", "/").replaceAll("~0", "~")
    value = object(value, `reference parent ${pointer}`)[key]
    if (value === undefined) fail(`unresolved published schema reference ${pointer}`)
  }
  return value
}

function inspect(value: unknown, root: unknown, path = "$", seen = new Set<unknown>()): void {
  if (typeof value !== "object" || value === null || seen.has(value)) return
  seen.add(value)
  if (Array.isArray(value)) {
    value.forEach((item, index) => inspect(item, root, `${path}[${index}]`, seen))
    return
  }
  for (const [key, item] of Object.entries(value)) {
    if (key === "~standard" || key === "def") fail(`published schema contains Zod implementation key ${path}.${key}`)
    if (key === "$ref" && typeof item === "string") resolvePointer(root, item)
    inspect(item, root, `${path}.${key}`, seen)
  }
}

for (const [toolName, exportedTool] of Object.entries(tools)) {
  const raw = tool.schema.toJSONSchema(tool.schema.object(exportedTool.args), { io: "input" })
  const root = object(raw, `${toolName} schema`)
  inspect(root, root)
  if (root.type !== "object") fail(`${toolName} schema root is not an object`)
  if (JSON.stringify(root.required) !== JSON.stringify(["request"])) fail(`${toolName} schema does not require only request`)
  const properties = object(root.properties, `${toolName} properties`)
  if (JSON.stringify(Object.keys(properties)) !== JSON.stringify(["request"])) fail(`${toolName} schema exposes fields outside request`)
  const request = object(properties.request, `${toolName} request`)
  const variants = request.oneOf
  if (!Array.isArray(variants)) fail(`${toolName} request does not publish oneOf variants`)
  const expected = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (variants.length !== expected.length) fail(`${toolName} publishes ${variants.length} variants for ${expected.length} operations`)
  for (const operation of expected as any[]) {
    const operationName = operation.id.slice(operation.id.indexOf(".") + 1)
    const variant = variants.find((candidate: any) => candidate?.properties?.operation?.const === operationName)
    if (!variant) fail(`${toolName} does not publish ${operationName}`)
    if (variant.additionalProperties !== false) fail(`${operation.id} request is not closed`)
    if (JSON.stringify(variant.required) !== JSON.stringify(["operation", "input"])) fail(`${operation.id} request fields are not required`)
    const input = resolvePointer(root, variant.properties.input.$ref)
    const schemaName = operation.input_schema.slice(operation.input_schema.lastIndexOf("/") + 1)
    if (JSON.stringify(input) !== JSON.stringify((payloadSchemas as Record<string, unknown>)[schemaName]).replaceAll("#/$defs/", "#/properties/request/definitions/")) {
      fail(`${operation.id} input differs from generated schema ${schemaName}`)
    }
  }
}

const workDefineRoot = object(tool.schema.toJSONSchema(tool.schema.object(work_define.args), { io: "input" }), "work define schema")
const workDefineRequest = object(object(workDefineRoot.properties, "work define properties").request, "work define request")
const capture = (workDefineRequest.oneOf as any[]).find((variant) => variant.properties.operation.const === "capture")
const captureInput = object(resolvePointer(workDefineRoot, capture.properties.input.$ref), "capture input")
const urgencyProperty = object(object(captureInput.properties, "capture properties").urgency, "capture urgency property")
const urgency = object(resolvePointer(workDefineRoot, urgencyProperty.$ref), "capture urgency")
if (JSON.stringify(urgency.enum) !== JSON.stringify(["standard", "expedite"])) fail("capture urgency enum is not published")

console.log(`host schema probe passed (${Object.keys(tools).length + 1} tools, ${contractOperations.length} operations)`)
