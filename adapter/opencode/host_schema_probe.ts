import { contractOperations, hostToolSchemas } from "./generated-contracts"
import { concord_ci_watch, publishCiWatchDefinition } from "./ci-watch"
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
inspect(workStartRoot)

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
    if (key === "$ref" || key === "anyOf" || key === "allOf" || key === "definitions") fail(`published schema contains host-unsafe ${key} at ${path}`)
    if (key === "oneOf") {
      // A oneOf is host-safe exactly when every branch is a self-contained
      // closed object the host renders directly: the bounded outcome_payload
      // variant union. Open, nested, or non-object unions stay merged.
      if (!Array.isArray(item)) fail(`published schema contains a non-list oneOf at ${path}`)
      for (const [index, branch] of item.entries()) {
        if (typeof branch !== "object" || branch === null || Array.isArray(branch)) fail(`published schema oneOf branch ${path}[${index}] is not an object`)
        if ((branch as Record<string, unknown>).additionalProperties !== false) fail(`published schema oneOf branch ${path}[${index}] is not closed`)
      }
    }
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
  // The published request is one closed branch per operation: the branch
  // names the operation with a const and its input states the required set
  // and the admitted fields the core enforces. The const keeps branches
  // mutually exclusive, and no branch admits a field a sibling operation owns.
  const requestBranches = request.oneOf
  if (!Array.isArray(requestBranches)) fail(`${toolName} request carries no per-operation branches`)
  if (requestBranches.length !== expected.length) fail(`${toolName} publishes ${requestBranches.length} request branches for ${expected.length} operations`)
  const operationEnum = operation.enum as string[]
  for (const [index, branch] of requestBranches.entries()) {
    if (typeof branch !== "object" || branch === null) fail(`${toolName} request branch ${index} is not an object`)
    const node = object(branch, `${toolName} request branch ${index}`)
    if (node.type !== "object" || node.additionalProperties !== false) fail(`${toolName} request branch ${index} is not a closed object`)
    if (JSON.stringify(node.required) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request branch ${index} does not require exactly operation and input`)
    const branchProperties = object(node.properties, `${toolName} request branch ${index} properties`)
    if (JSON.stringify(Object.keys(branchProperties)) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request branch ${index} exposes fields outside operation and input`)
    const branchOperation = object(branchProperties.operation, `${toolName} request branch ${index} operation`)
    if (branchOperation.const !== operationEnum[index]) fail(`${toolName} request branch ${index} does not name ${operationEnum[index]}`)
    const input = object(branchProperties.input, `${toolName} request branch ${index} input`)
    if (input.type !== "object") fail(`${toolName} request branch ${index} input is not an object`)
    const branchRequired: unknown = input.required
    if (!Array.isArray(branchRequired)) fail(`${toolName} input branch ${index} states no required set`)
    const inputProperties = object(input.properties, `${toolName} input branch ${index} properties`)
    if (Object.keys(inputProperties).length === 0) fail(`${toolName} input branch ${index} names no fields`)
    if (input.additionalProperties !== false) fail(`${toolName} input branch ${index} is not closed`)
    for (const name of branchRequired) {
      if (!(name in inputProperties)) fail(`${toolName} input branch ${index} requires unknown field ${name}`)
    }
  }
}

const workDefineRoot = publishedArgsSchema(work_define.args, "work define schema")
const workDefineRequest = object(object(workDefineRoot.properties, "work define properties").request, "work define request")
const captureInput = object(object(object(object(workDefineRequest.oneOf[0], "capture request branch").properties, "capture request properties").input, "capture input"), "capture input")
if (JSON.stringify(captureInput.required) !== JSON.stringify(["title", "value_statement", "kind", "project_ids", "idempotency_key"])) fail("capture required set does not match the generated contract")
const urgency = object(object(captureInput.properties, "capture input properties").urgency, "capture urgency")
if (JSON.stringify(urgency.enum) !== JSON.stringify(["standard", "expedite"])) fail("capture urgency enum is not published")

// The transition action branch must name every field the core admits for the
// workflow_action operation, including the conditional ones.
const transitionRoot = publishedArgsSchema(work_transition.args, "work transition schema")
const transitionRequest = object(object(transitionRoot.properties, "work transition properties").request, "work transition request")
if (!Array.isArray(transitionRequest.oneOf)) fail("work transition request carries no branch list")
const transitionBranches = (transitionRequest.oneOf as unknown[]).map((branch) => object(branch, "action request branch"))
const actionBranch = transitionBranches.find((branch) => object(branch.properties, "action request properties").operation.const === "workflow_action")
if (actionBranch === undefined) fail("concord_work_transition publishes no workflow_action branch")
const actionProperties = Object.keys(object(object(actionBranch.properties, "action request properties").input, "action input").properties)
for (const field of ["action_id", "selected_choice", "decision_context_digest", "fields", "requested_budget_seconds"]) {
  if (!actionProperties.includes(field)) fail(`the published workflow_action branch does not name ${field}`)
}

// The watcher is a direct tool: its args are the published argument fields
// themselves, not a root schema. The host publishes each key of args as one
// property of the tool's object schema, so repo and selector must appear as
// fields and never as schema keywords. The host requires every field before
// tool.definition runs, so the probe applies the production hook to that view.
const ciWatchDefinition = {
  description: concord_ci_watch.description,
  parameters: {},
  jsonSchema: publishedArgsSchema(concord_ci_watch.args as Record<string, unknown>, "ci watch schema"),
}
await publishCiWatchDefinition({ toolID: "concord_ci_watch" }, ciWatchDefinition)
const ciWatchRoot = object(ciWatchDefinition.jsonSchema, "published ci watch schema")
inspect(ciWatchRoot)
const ciWatchProperties = object(ciWatchRoot.properties, "ci watch properties")
if (
  JSON.stringify(Object.keys(ciWatchProperties).sort()) !==
  JSON.stringify(["mode", "repo", "selector", "time_seconds_max"])
) {
  fail("concord_ci_watch does not publish repo, selector, mode, and time_seconds_max as argument fields")
}
if (JSON.stringify(ciWatchRoot.required) !== JSON.stringify(["repo", "selector"])) fail("concord_ci_watch does not require repo and selector")
const ciWatchSelector = object(ciWatchProperties.selector, "ci watch selector")
if (JSON.stringify(ciWatchSelector.required) !== JSON.stringify(["kind", "value"])) fail("concord_ci_watch selector does not require kind and value")

function publishedArgsSchema(args: Record<string, unknown>, label: string, required = Object.keys(args)): Record<string, any> {
  const properties = Object.fromEntries(Object.entries(args).filter(([, value]) => value !== undefined))
  return object({ type: "object", properties, required }, label)
}

console.log(`host schema probe passed (${Object.keys(tools).length + 1} tools, ${contractOperations.length} operations)`)
