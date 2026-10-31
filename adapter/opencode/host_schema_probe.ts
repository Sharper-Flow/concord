import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { contractOperations, hostToolSchemas, workflowActionPublicVariants } from "./generated-contracts"
import { expandedPublishedRequestSchema } from "./generated-contract-tests"
import { roleDeniedConcordTools } from "./generated-primary-profiles"
import { domain, knowledge, product_view, publishRequestDefinition, publishWorkStartDefinition, work_browse, work_compact, work_define, work_relate, work_start, work_trace, work_transition } from "./concord"

const tools: Record<string, any> = {
  concord_product_view: product_view,
  concord_work_browse: work_browse,
  concord_work_trace: work_trace,
  concord_knowledge: knowledge,
  concord_work_define: work_define,
  concord_domain: domain,
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
// Publication interns repeated field schemas into the root's local
// definitions table, so closure is proven on the raw root and the branch
// comparison reads the resolved view against the same generated branches.
const workStartRoot = object(workStartDefinition.jsonSchema, "published work start schema")
if (workStartRoot.type !== "object") fail("concord_work_start schema root is not an object")
const registrationBytes: Record<string, number> = { concord_work_start: Buffer.byteLength(JSON.stringify(workStartRoot), "utf8") }
for (const keyword of ["oneOf", "anyOf", "allOf"]) {
  if (Object.hasOwn(workStartRoot, keyword)) fail(`concord_work_start carries provider-unsafe root ${keyword}`)
}
if (JSON.stringify(workStartRoot.if) !== JSON.stringify({ required: ["work_id"] })) fail("concord_work_start does not select resume by work_id presence")
const workStartIssueKeySelector = object(workStartRoot.else, "published work start issue_key selector")
if (JSON.stringify(workStartIssueKeySelector.if) !== JSON.stringify({ required: ["issue_key"] })) fail("concord_work_start does not select resume by issue_key presence")
checkLocalRefs(workStartRoot, "concord_work_start")
const expandedWorkStart: any = expandedPublishedRequestSchema(workStartRoot)
const publishedBranches = [expandedWorkStart.else?.else, expandedWorkStart.then, expandedWorkStart.else?.then].map((branch) => object(branch, "published work start branch"))
for (const [index, branch] of publishedBranches.entries()) {
  if (JSON.stringify(branch) !== JSON.stringify(expandedPublishedRequestSchema(expectedWorkStartBranches[Math.min(index, 1)]))) fail(`published work start branch ${index} differs from its generated closed contract`)
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
    if (key === "definitions") fail(`published schema contains host-unsafe definitions at ${path}`)
    if (key === "allOf") fail(`published schema contains host-unsafe ${key} at ${path}`)
    // Publication factors shared structure into local references.
    // Only the local form is admitted here; each final registered root
    // proves below that every reference it carries resolves acyclically
    // inside its own definitions table.
    if (key === "$ref" && (typeof item !== "string" || !item.startsWith("#/$defs/"))) {
      fail(`published schema carries a non-local reference at ${path}`)
    }
    // anyOf survives only as an authored cross-field exclusion nested inside not.
    // A bare anyOf anywhere else is still a merge fault.
    if (key === "anyOf" && !insideNot) fail(`published schema contains host-unsafe ${key} at ${path}`)
    if (key === "oneOf") {
      // Publication emits authored structure verbatim, and the authored
      // contracts use oneOf in every documented form: bounded closed-object
      // variant unions (outcome_payload), legal-combination condition
      // members (the resolve product/project selector), scalar type
      // alternations (lesson issue identifiers), and local references. Each
      // member must be a schema object; the closed-branch convention applies
      // to the per-operation request unions the expanded view checks below.
      if (!Array.isArray(item)) fail(`published schema contains a non-list oneOf at ${path}`)
      for (const [index, branch] of item.entries()) {
        if (typeof branch !== "object" || branch === null || Array.isArray(branch)) fail(`published schema oneOf branch ${path}[${index}] is not an object`)
      }
    }
    inspect(item, `${path}.${key}`, seen, insideNot || key === "not")
  }
}

// checkLocalRefs proves one final registered root self-contained: every
// reference it carries is local, resolves inside its own definitions table,
// and no chain cycles. The probe never reconstructs definitions; it reads
// the root the production hook published.
function checkLocalRefs(rootValue: unknown, label: string): void {
  const root = object(rootValue, label)
  const defs = root.$defs
  if (defs === null || typeof defs !== "object" || Array.isArray(defs)) fail(`${label} publishes no definitions table for its references`)
  const names = new Set(Object.keys(defs))
  const state = new Map<string, "visiting" | "done">()
  const resolve = (name: string): void => {
    const mark = state.get(name)
    if (mark === "done") return
    if (mark === "visiting") fail(`${label} carries a cyclic reference chain through #/$defs/${name}`)
    if (!names.has(name)) fail(`${label} carries the unresolvable reference #/$defs/${name}`)
    state.set(name, "visiting")
    walk(defs[name])
    state.set(name, "done")
  }
  const walk = (value: unknown): void => {
    if (Array.isArray(value)) {
      value.forEach(walk)
      return
    }
    if (value === null || typeof value !== "object") return
    for (const [key, child] of Object.entries(value)) {
      if (key === "$ref") {
        if (typeof child !== "string" || !child.startsWith("#/$defs/")) fail(`${label} carries the non-local reference ${JSON.stringify(child)}`)
        resolve(child.slice("#/$defs/".length))
        continue
      }
      walk(child)
    }
  }
  walk(root)
}

for (const [toolName, exportedTool] of Object.entries(tools)) {
  // The registration map keeps one request argument per request tool: the
  // compact publication changes schema bytes, never the registered argument
  // names. The final registered root comes from the production definition
  // hook, which hoists the request definitions onto the argument root.
  const registration = publishedArgsSchema(exportedTool.args, `${toolName} registration schema`)
  inspect(registration)
  if (registration.type !== "object") fail(`${toolName} registration schema root is not an object`)
  if (JSON.stringify(registration.required) !== JSON.stringify(["request"])) fail(`${toolName} schema does not require only request`)
  const registrationProperties = object(registration.properties, `${toolName} properties`)
  if (JSON.stringify(Object.keys(registrationProperties)) !== JSON.stringify(["request"])) fail(`${toolName} schema exposes fields outside request`)

  const definition = { description: exportedTool.description, parameters: {}, jsonSchema: registration }
  await publishRequestDefinition({ toolID: toolName }, definition)
  const root = object(definition.jsonSchema, `${toolName} published root`)
  inspect(root)
  checkLocalRefs(root, toolName)
  registrationBytes[toolName] = Buffer.byteLength(JSON.stringify(root), "utf8")
  if (root.type !== "object") fail(`${toolName} schema root is not an object`)
  for (const keyword of ["oneOf", "anyOf", "allOf"]) {
    if (Object.hasOwn(root, keyword)) fail(`${toolName} carries provider-unsafe root ${keyword}`)
  }
  if (JSON.stringify(root.required) !== JSON.stringify(["request"])) fail(`${toolName} published root does not require only request`)
  const rootProperties = object(root.properties, `${toolName} published properties`)
  if (JSON.stringify(Object.keys(rootProperties)) !== JSON.stringify(["request"])) fail(`${toolName} published root exposes fields outside request`)
  if (Object.hasOwn(rootProperties.request, "$defs")) fail(`${toolName} keeps its definitions inside the request instead of the argument root`)
  const request = object(rootProperties.request, `${toolName} request`)
  const expected = contractOperations.filter((operation: any) => operation.tool === toolName)
  if (JSON.stringify(request.required) !== JSON.stringify(["operation", "input"])) fail(`${toolName} request fields are not required`)
  const operation = object(request.properties.operation, `${toolName} operation`)
  if (JSON.stringify(operation.enum) !== JSON.stringify(expected.map((candidate: any) => candidate.id.slice(candidate.id.indexOf(".") + 1)))) fail(`${toolName} operation enum differs from the generated contract`)
  // The compact request carries one branch per operation, workflow_action
  // included; the grouped action variants live under that branch's input.
  const compactBranches = request.oneOf
  if (!Array.isArray(compactBranches)) fail(`${toolName} request carries no per-operation branches`)
  if (compactBranches.length !== expected.length) fail(`${toolName} publishes ${compactBranches.length} compact branches for ${expected.length} operations`)
  // The effective view resolves the references and inherits each factored
  // union parent's constraints, so the obligations below keep reading the
  // legacy branch list: one closed branch per operation, and for
  // workflow_action one closed branch per registry action variant, in
  // registry order. No branch admits a field a sibling operation or action
  // owns.
  const expanded: any = expandedPublishedRequestSchema(root)
  const requestBranches = expanded.oneOf
  if (!Array.isArray(requestBranches)) fail(`${toolName} request expands to no per-operation branches`)
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

const workDefineDefinition = { description: work_define.description, parameters: {}, jsonSchema: publishedArgsSchema(work_define.args, "work define schema") }
await publishRequestDefinition({ toolID: "concord_work_define" }, workDefineDefinition)
const workDefineExpanded: any = expandedPublishedRequestSchema(workDefineDefinition.jsonSchema)
const captureBranch = workDefineExpanded.oneOf.find((candidate: any) => candidate.properties?.operation?.const === "capture")
if (captureBranch === undefined) fail("work define publishes no capture branch")
const captureInput = object(captureBranch.properties.input, "capture input")
if (JSON.stringify(captureInput.required) !== JSON.stringify(["title", "value_statement", "kind", "project_ids", "idempotency_key"])) fail("capture required set does not match the generated contract")
const urgency = object(object(captureInput.properties, "capture input properties").urgency, "capture urgency")
if (JSON.stringify(urgency.enum) !== JSON.stringify(["standard", "expedite"])) fail("capture urgency enum is not published")

// Every workflow action variant branch must name the fields the core admits
// for that action, including the conditional ones, and each action the
// registry declares must have exactly one branch, in registry order.
const transitionDefinition = { description: work_transition.description, parameters: {}, jsonSchema: publishedArgsSchema(work_transition.args, "work transition schema") }
await publishRequestDefinition({ toolID: "concord_work_transition" }, transitionDefinition)
const transitionExpanded: any = expandedPublishedRequestSchema(transitionDefinition.jsonSchema)
if (!Array.isArray(transitionExpanded.oneOf)) fail("work transition request carries no branch list")
const transitionBranches = (transitionExpanded.oneOf as unknown[]).map((branch) => object(branch, "action request branch"))
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
  for (const field of ["action_id", "requested_budget_seconds"]) {
    if (!actionProperties.includes(field)) fail(`the published ${actionConst} variant does not name ${field}`)
  }
}


// Opt-in real-host permission-merge evidence: `--host-merge <agents-dir>
// <summary.json> [binary]` resolves every generated role's permissions
// through the installed host's `debug agent <role>` route in an isolated
// fixture (fresh HOME/XDG under the OS temp dir, whitelist environment,
// --pure, cwd outside any repository, the exact generated agent files). The
// evidence kind is resolved profile permissions, never a captured provider
// request or a live session ruleset. Besides the no-overlay pass it
// demonstrates, on real host merges, one deny overlay and one allow
// override at the agent/global-config level; session-specific overlays are
// not exercised, so the no-overlay projection stays the accepted acceptance
// boundary, not an immutable ceiling. Every host command runs through one
// isolated-invocation helper bounded by a 30-second native spawn timeout,
// and a failed command reports the child's complete stderr; the summary
// records the host version the same isolated environment and binary
// resolved; the repository dependency pin remains independent. Without the
// flag this file runs only the portable checks above and never requires the
// installed host.
const mergeArgIndex = process.argv.indexOf("--host-merge")
if (mergeArgIndex !== -1) {
  const [agentsDir, summaryPath, binaryArg] = process.argv.slice(mergeArgIndex + 1)
  if (!agentsDir || !summaryPath) fail("--host-merge requires <agents-dir> <summary.json> [binary]")
  const binary = binaryArg ?? process.env.OPENCODE_BIN ?? Bun.which("opencode")
  if (!binary) fail("--host-merge resolved no host binary: pass one or set OPENCODE_BIN")
  const catalog = [...new Set([...contractOperations.map((operation: any) => operation.tool), ...Object.keys(hostToolSchemas)])].sort()
  // Role projections and the summary total sum measured roots only: the
  // accounting must cover the catalog exactly, and every root must be a
  // finite positive byte count, before any total is formed.
  if (JSON.stringify(Object.keys(registrationBytes).sort()) !== JSON.stringify(catalog)) {
    fail("registration byte accounting does not cover the registered catalog exactly")
  }
  for (const [tool, size] of Object.entries(registrationBytes)) {
    if (!Number.isFinite(size) || size <= 0) fail(`${tool} registration size is not a finite positive byte count: ${size}`)
  }
  // mkdtemp owns name uniqueness, and every directory this mode creates -
  // the agent fixture copy included - sits inside the try, so a failure
  // during the fixture build still removes the whole isolation tree.
  const isolation = fs.mkdtempSync(path.join(os.tmpdir(), "concord-host-merge-"))
  try {
    const fixture = path.join(isolation, "fixture")
    fs.mkdirSync(path.join(fixture, ".opencode/agents"), { recursive: true })
    const home = path.join(isolation, "home")
    for (const dir of [".config", ".local/share", ".cache"]) fs.mkdirSync(path.join(home, dir), { recursive: true })
    for (const role of Object.keys(roleDeniedConcordTools)) {
      const origin = path.join(agentsDir, `${role}.md`)
      if (!fs.existsSync(origin)) fail(`--host-merge: generated agent definition missing: ${role}.md`)
      fs.copyFileSync(origin, path.join(fixture, ".opencode/agents", `${role}.md`))
    }
    // One isolated invocation helper for every host command in this mode:
    // the same whitelist environment shape, the same fixture root as cwd,
    // and a native spawn timeout so a hung host command ends after 30
    // seconds instead of parking the probe.
    const hostEnvironment = (agentHome: string): Record<string, string> => ({ PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: agentHome, XDG_CONFIG_HOME: `${agentHome}/.config`, XDG_DATA_HOME: `${agentHome}/.local/share`, XDG_CACHE_HOME: `${agentHome}/.cache`, TMPDIR: agentHome })
    const runHost = async (args: string[], cwd: string, agentHome: string): Promise<{ stdout: string; stderr: string; exitCode: number }> => {
      const child = Bun.spawn([binary, "--pure", ...args], { cwd, env: hostEnvironment(agentHome), stdout: "pipe", stderr: "pipe", timeout: 30_000 })
      const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
      return { stdout, stderr, exitCode }
    }
    const versionRun = await runHost(["--version"], fixture, home)
    if (versionRun.exitCode !== 0) fail(`host --version exited ${versionRun.exitCode}:\n${versionRun.stderr}`)
    const binaryVersion = versionRun.stdout.split("\n")[0]?.trim() ?? ""
    if (!binaryVersion) fail("host --version produced no version output")
    // The reducer of record for host-resolved permission rules. It reduces
    // only the exact flat rule shapes the generated configuration can
    // produce: a global '*' fallback carrying pattern '*', then exact
    // registered Concord tool names carrying pattern '*', where the later
    // rule wins. A patterned or wildcard scope, a later '*', or a Concord
    // name outside the catalog refuses instead of being silently accepted;
    // every other permission name carries host pattern semantics this
    // reducer does not model, so it ignores those rules rather than
    // misreading them as Concord decisions.
    const runAgent = async (role: string, agentFixture: string, agentHome: string): Promise<Record<string, string>> => {
      const { stdout, stderr, exitCode } = await runHost(["debug", "agent", role], agentFixture, agentHome)
      if (exitCode !== 0) fail(`debug agent ${role} exited ${exitCode}:\n${stderr}`)
      let output: any
      try {
        output = JSON.parse(stdout)
      } catch (error) {
        fail(`debug agent ${role} emitted unparseable output (${(error as Error).message}):\nstdout:\n${stdout}\nstderr:\n${stderr}`)
      }
      if (output === null || typeof output !== "object" || Array.isArray(output) || output.name !== role) {
        fail(`debug agent ${role} did not resolve the requested role: ${JSON.stringify(output?.name)}`)
      }
      if (!Array.isArray(output.permission)) fail(`debug agent ${role} carries no permission rule list`)
      const actions: Record<string, string> = {}
      let fallbackAction = ""
      let fallbackIndex = -1
      let concordIndex = -1
      for (const [index, rule] of output.permission.entries()) {
        if (rule === null || typeof rule !== "object" || typeof rule.permission !== "string" || typeof rule.action !== "string" || typeof rule.pattern !== "string") {
          fail(`debug agent ${role} carries a malformed permission rule: ${JSON.stringify(rule)}`)
        }
        if (rule.action !== "allow" && rule.action !== "deny" && rule.action !== "ask") {
          fail(`debug agent ${role} carries rule action ${JSON.stringify(rule.action)} outside allow/deny/ask`)
        }
        if (rule.permission === "*") {
          if (rule.pattern !== "*") fail(`debug agent ${role} carries a global fallback rule with pattern ${JSON.stringify(rule.pattern)}`)
          if (concordIndex !== -1) fail(`debug agent ${role} carries a global '*' rule after its first Concord-specific rule; this reducer does not emulate that ordering`)
          fallbackAction = rule.action
          fallbackIndex = index
          continue
        }
        if (!rule.permission.startsWith("concord_")) continue
        if (!catalog.includes(rule.permission)) fail(`debug agent ${role} carries Concord permission ${rule.permission} outside the registered catalog`)
        if (rule.pattern !== "*") fail(`debug agent ${role} carries patterned Concord scope ${JSON.stringify(rule.pattern)} for ${rule.permission}`)
        if (concordIndex === -1) concordIndex = index
        actions[rule.permission] = rule.action
      }
      if (fallbackIndex === -1) fail(`debug agent ${role} carries no builtin global default rule`)
      const resolved: Record<string, string> = {}
      for (const tool of catalog) resolved[tool] = actions[tool] ?? fallbackAction
      return resolved
    }
    const assertResolved = (role: string, resolved: Record<string, string>): void => {
      for (const tool of catalog) {
        const denied = roleDeniedConcordTools[role].tools.includes(tool)
        if (denied && resolved[tool] !== "deny") fail(`${role}: ${tool} must resolve deny, the host resolved ${resolved[tool]}`)
        if (!denied && resolved[tool] !== "allow") fail(`${role}: ${tool} must resolve allow (no generated profile declares a Concord ask), the host resolved ${resolved[tool]}`)
      }
    }
    const noOverlay: Record<string, { resolved: Record<string, string>; projected_bytes: number; allowed_tools: string[] }> = {}
    for (const role of Object.keys(roleDeniedConcordTools).sort()) {
      const resolved = await runAgent(role, fixture, home)
      assertResolved(role, resolved)
      const allowed = catalog.filter((tool) => !roleDeniedConcordTools[role].tools.includes(tool))
      noOverlay[role] = { resolved, projected_bytes: allowed.reduce((sum, tool) => sum + registrationBytes[tool], 0), allowed_tools: allowed }
    }
    // Deny overlay, agent level: the operator's frontmatter gains a reader
    // deny, so the host merge must drop a tool its no-overlay profile allowed.
    const denyFixture = path.join(isolation, "deny-fixture")
    fs.cpSync(fixture, denyFixture, { recursive: true })
    const operatorPath = path.join(denyFixture, ".opencode/agents/Concord.md")
    const operatorText = fs.readFileSync(operatorPath, "utf8")
    if (!operatorText.includes("\npermission:\n")) fail("generated operator frontmatter lost its permission block")
    fs.writeFileSync(operatorPath, operatorText.replace("\npermission:\n", "\npermission:\n  concord_domain: deny\n"))
    const denyResolved = await runAgent("Concord", denyFixture, home)
    if (denyResolved.concord_domain !== "deny") fail(`deny overlay: the host still resolves concord_domain as ${denyResolved.concord_domain}`)
    // Allow override, agent level over a global-config deny: a utility whose
    // frontmatter allows the reader regains it from the later agent rule.
    // This is the growth counterexample: the utility's projected bytes grow by
    // the reader root, so no-overlay zero is visibility, not a ceiling.
    const allowFixture = path.join(isolation, "allow-fixture")
    fs.cpSync(fixture, allowFixture, { recursive: true })
    const allowHome = path.join(isolation, "allow-home")
    fs.mkdirSync(path.join(allowHome, ".config/opencode"), { recursive: true })
    fs.writeFileSync(path.join(allowHome, ".config/opencode/opencode.json"), JSON.stringify({ permission: { concord_domain: "deny" } }))
    const explorePath = path.join(allowFixture, ".opencode/agents/concord-explore.md")
    const exploreText = fs.readFileSync(explorePath, "utf8")
    if (!exploreText.includes("  concord_domain: false\n")) fail("generated explore frontmatter lost its reader tools entry")
    fs.writeFileSync(explorePath, exploreText.replaceAll("  concord_domain: false\n", "  concord_domain: true\n"))
    const allowResolved = await runAgent("concord-explore", allowFixture, allowHome)
    if (allowResolved.concord_domain !== "allow") fail(`allow override: the host still resolves concord_domain as ${allowResolved.concord_domain}`)
    const summary = {
      probe: "concord-role-publication-host-merge-v1",
      binary: path.basename(binary),
      binary_version: binaryVersion,
      evidence_kind: "resolved profile permissions from the host debug agent route; not a captured provider request and not a live session ruleset",
      registration: { tools: registrationBytes, total_bytes: Object.values(registrationBytes).reduce((sum, size) => sum + size, 0) },
      roles: noOverlay,
      deny_overlay_agent_level: { role: "Concord", change: "frontmatter permission gains concord_domain: deny", resolved_reader: denyResolved.concord_domain },
      allow_override_agent_level: { role: "concord-explore", change: "frontmatter tools entry allows the reader over a global-config deny", resolved_reader: allowResolved.concord_domain, publication_growth_bytes: registrationBytes.concord_domain },
      bounds: { session_overlays: "not exercised; the no-overlay projection is the accepted acceptance boundary, not an immutable ceiling" },
    }
    fs.mkdirSync(path.dirname(path.resolve(summaryPath)), { recursive: true })
    fs.writeFileSync(summaryPath, `${JSON.stringify(summary, null, 2)}\n`)
    console.log(`host permission-merge probe passed (${Object.keys(noOverlay).length} roles, deny overlay and allow override resolved on ${path.basename(binary)} ${binaryVersion})`)
  } finally {
    fs.rmSync(isolation, { recursive: true, force: true })
  }
}

function publishedArgsSchema(args: Record<string, unknown>, label: string, required = Object.keys(args)): Record<string, any> {
  const properties = Object.fromEntries(Object.entries(args).filter(([, value]) => value !== undefined))
  return object({ type: "object", properties, required }, label)
}

console.log(`host schema probe passed (${Object.keys(tools).length + 2} tools, ${contractOperations.length} operations)`)
