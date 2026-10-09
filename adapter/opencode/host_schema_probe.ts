import { contractOperations, hostToolSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { ciWatchTool, publishCiWatchDefinition } from "./ci-watch"
import { domain, knowledge, product_view, publishWorkStartDefinition, work_browse, work_compact, work_define, work_initiative, work_relate, work_start, work_trace, work_transition } from "./concord"

const concord_ci_watch = ciWatchTool()

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

// Apply the production hook to the registration view. The expected contract
// is used for comparison only, never to repair the observed schema.
const expectedWorkStart = object(hostToolSchemas.concord_work_start, "generated work start schema")
const expectedWorkStartBranches = (expectedWorkStart.oneOf as any[]).filter((branch) => object(branch, "generated work start branch"))
if (expectedWorkStartBranches.length !== 2) fail("generated work start schema must carry the capture and resume branches")
const workStartDefinition = {
  description: work_start.description,
  parameters: {},
  jsonSchema: undefined as unknown,
}
await publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
// The root is provider-compatible; its conditional branches retain the
// generated capture/resume contract without losing requireds or exclusions.
const workStartRoot = object(workStartDefinition.jsonSchema, "published work start schema")
if (workStartRoot.type !== "object") fail("concord_work_start schema root is not an object")
for (const keyword of ["oneOf", "anyOf", "allOf"]) {
  if (Object.hasOwn(workStartRoot, keyword)) fail(`concord_work_start carries provider-unsafe root ${keyword}`)
}
if (JSON.stringify(workStartRoot.if) !== JSON.stringify({ required: ["work_id"] })) fail("concord_work_start does not select resume by work_id presence")
const publishedBranches = [workStartRoot.else, workStartRoot.then].map((branch) => object(branch, "published work start branch"))
for (const [index, branch] of publishedBranches.entries()) {
  if (JSON.stringify(branch) !== JSON.stringify(expectedWorkStartBranches[index])) fail(`published work start branch ${index} differs from its generated closed contract`)
}
inspect(workStartRoot)
// The registration map is the host's per-parameter rendering channel; its
// field schemas must still match the generated branches exactly.
const workStartRegistration = publishedArgsSchema(work_start.args, "work start registration schema")
inspect(workStartRegistration)

function fail(message: string): never {
  throw new Error(message)
}

function object(value: unknown, label: string): Record<string, any> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail(`${label} is not an object`)
  return value as Record<string, any>
}

function inspect(value: unknown, path = "$", seen = new Set<unknown>(), insideNot = false): void {
  if (typeof value !== "object" || value === null || seen.has(value)) return
  seen.add(value)
  if (Array.isArray(value)) {
    value.forEach((item, index) => inspect(item, `${path}[${index}]`, seen, insideNot))
    return
  }
  for (const [key, item] of Object.entries(value)) {
    if (key === "~standard" || key === "def") fail(`published schema contains Zod implementation key ${path}.${key}`)
    if (key === "$ref" || key === "definitions" || key === "allOf") fail(`published schema contains host-unsafe ${key} at ${path}`)
    // anyOf survives only as an authored cross-field exclusion nested inside
    // not (the action variants' selected_choice/decision_context_digest rule).
    // A bare anyOf anywhere else is still a merge fault.
    if (key === "anyOf" && !insideNot) fail(`published schema contains host-unsafe ${key} at ${path}`)
    if (key === "oneOf") {
      // Publication emits authored structure verbatim, and the authored
      // contracts use oneOf in every documented form: bounded closed-object
      // variant unions (outcome_payload), legal-combination condition
      // members (the resolve product/project selector), scalar type
      // alternations (lesson issue identifiers), and inlined refs. Each
      // member must be a schema object; the closed-branch convention applies
      // to the per-operation request unions this probe checks separately.
      if (!Array.isArray(item)) fail(`published schema contains a non-list oneOf at ${path}`)
      for (const [index, branch] of item.entries()) {
        if (typeof branch !== "object" || branch === null || Array.isArray(branch)) fail(`published schema oneOf branch ${path}[${index}] is not an object`)
      }
    }
    inspect(item, `${path}.${key}`, seen, insideNot || key === "not")
  }
}

for (const [toolName, exportedTool] of Object.entries(tools)) {
  const root = publishedArgsSchema(exportedTool.args, `${toolName} schema`)
  inspect(root)
  if (root.type !== "object") fail(`${toolName} schema root is not an object`)
  for (const keyword of ["oneOf", "anyOf", "allOf"]) {
    if (Object.hasOwn(root, keyword)) fail(`${toolName} carries provider-unsafe root ${keyword}`)
  }
  if (JSON.stringify(root.required) !== JSON.stringify(["request"])) fail(`${toolName} schema does not require only request`)
  const properties = object(root.properties, `${toolName} properties`)
  if (JSON.stringify(Object.keys(properties)) !== JSON.stringify(["request"])) fail(`${toolName} schema exposes fields outside request`)
  const request = object(properties.request, `${toolName} request`)
  const expected = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (JSON.stringify(request.required) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request fields are not required`)
  const operation = object(request.properties.operation, `${toolName} operation`)
  if (JSON.stringify(operation.enum) !== JSON.stringify(expected.map((candidate: any) => candidate.id.slice(candidate.id.indexOf(".") + 1)))) fail(`${toolName} operation enum differs from the generated contract`)
  // The published request is one closed branch per operation — and for
  // workflow_action, one closed branch per registry action variant: the
  // branch names the operation (and action) with a const, and its input
  // states the required set and the admitted fields the core enforces. The
  // const keeps branches mutually exclusive, and no branch admits a field a
  // sibling operation or action owns.
  const requestBranches = request.oneOf
  if (!Array.isArray(requestBranches)) fail(`${toolName} request carries no per-operation branches`)
  const variantExtra = toolName === "concord_work_transition" ? workflowActionPublicVariants.length - 1 : 0
  if (requestBranches.length !== expected.length + variantExtra) fail(`${toolName} publishes ${requestBranches.length} request branches for ${expected.length + variantExtra} closed shapes`)
  const operationEnum = operation.enum as string[]
  let operationIndex = -1
  let inVariantRun = false
  for (const [index, branch] of requestBranches.entries()) {
    if (typeof branch !== "object" || branch === null) fail(`${toolName} request branch ${index} is not an object`)
    const node = object(branch, `${toolName} request branch ${index}`)
    if (node.type !== "object" || node.additionalProperties !== false) fail(`${toolName} request branch ${index} is not a closed object`)
    if (JSON.stringify(node.required) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request branch ${index} does not require exactly operation and input`)
    const branchProperties = object(node.properties, `${toolName} request branch ${index} properties`)
    if (JSON.stringify(Object.keys(branchProperties)) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request branch ${index} exposes fields outside operation and input`)
    const branchOperation = object(branchProperties.operation, `${toolName} request branch ${index} operation`)
    const input = object(branchProperties.input, `${toolName} request branch ${index} input`)
    if (branchOperation.const === "workflow_action" && input.properties?.action_id?.const !== undefined) {
      // An action variant branch names its action with a const. The whole
      // variant run sits at the workflow_action position in operation order;
      // the run consumes that one discriminator entry.
      if (operationEnum[operationIndex + 1] !== "workflow_action") fail(`${toolName} action variant branch ${index} does not sit at the workflow_action discriminator`)
      if (!workflowActionPublicVariants.some((candidate) => candidate.action_id === input.properties.action_id.const)) fail(`${toolName} request branch ${index} names action ${input.properties.action_id.const} no registry variant declares`)
      inVariantRun = true
      continue
    }
    if (inVariantRun) {
      operationIndex += 1
      inVariantRun = false
    }
    operationIndex += 1
    if (branchOperation.const !== operationEnum[operationIndex]) fail(`${toolName} request branch ${index} does not name ${operationEnum[operationIndex]}`)
    if (input.type !== "object") fail(`${toolName} request branch ${index} input is not an object`)
    const branchRequired: unknown = input.required
    // required is the authored set exactly: an operation that requires
    // nothing publishes no required array.
    if (branchRequired !== undefined && !Array.isArray(branchRequired)) fail(`${toolName} input branch ${index} states a non-list required set`)
    const inputProperties = object(input.properties, `${toolName} input branch ${index} properties`)
    if (Object.keys(inputProperties).length === 0) fail(`${toolName} input branch ${index} names no fields`)
    if (input.additionalProperties !== false) fail(`${toolName} input branch ${index} is not closed`)
    for (const name of Array.isArray(branchRequired) ? branchRequired : []) {
      if (!(name in inputProperties)) fail(`${toolName} input branch ${index} requires unknown field ${name}`)
    }
  }
  if (inVariantRun) operationIndex += 1
  if (operationIndex !== operationEnum.length - 1) fail(`${toolName} publishes branches for only ${operationIndex + 1} of ${operationEnum.length} operations`)
}

const workDefineRoot = publishedArgsSchema(work_define.args, "work define schema")
const workDefineRequest = object(object(workDefineRoot.properties, "work define properties").request, "work define request")
const captureInput = object(object(object(object(workDefineRequest.oneOf[0], "capture request branch").properties, "capture request properties").input, "capture input"), "capture input")
if (JSON.stringify(captureInput.required) !== JSON.stringify(["title", "value_statement", "kind", "project_ids", "idempotency_key"])) fail("capture required set does not match the generated contract")
const urgency = object(object(captureInput.properties, "capture input properties").urgency, "capture urgency")
if (JSON.stringify(urgency.enum) !== JSON.stringify(["standard", "expedite"])) fail("capture urgency enum is not published")

// Every workflow action variant branch must name the fields the core admits
// for that action, including the conditional ones, and each action the
// registry declares must have exactly one branch.
const transitionRoot = publishedArgsSchema(work_transition.args, "work transition schema")
const transitionRequest = object(object(transitionRoot.properties, "work transition properties").request, "work transition request")
if (!Array.isArray(transitionRequest.oneOf)) fail("work transition request carries no branch list")
const transitionBranches = (transitionRequest.oneOf as unknown[]).map((branch) => object(branch, "action request branch"))
const publishedActionIds = transitionBranches
  .filter((branch) => object(branch.properties, "action request properties").operation.const === "workflow_action")
  .map((branch) => object(object(branch.properties, "action request properties").input, "action input").properties.action_id.const)
if (JSON.stringify(publishedActionIds) !== JSON.stringify(workflowActionPublicVariants.map((variant) => variant.action_id))) {
  fail("concord_work_transition does not publish exactly one closed branch per registry action, in registry order")
}
for (const branch of transitionBranches) {
  const actionInput = object(object(branch.properties, "action request properties").input, "action input")
  const actionConst = (actionInput.properties as Record<string, any>).action_id?.const
  if (actionConst === undefined) continue
  const actionProperties = Object.keys((actionInput.properties as Record<string, unknown>))
  for (const field of ["action_id", "selected_choice", "decision_context_digest", "requested_budget_seconds"]) {
    if (!actionProperties.includes(field)) fail(`the published ${actionConst} variant does not name ${field}`)
  }
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
