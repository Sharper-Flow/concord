import { hostToolDescriptions, hostToolSchemas } from "./generated-contracts"
import { domain, knowledge, product_view, publishWorkStartDefinition, work_browse, work_compact, work_define, work_initiative, work_relate, work_start, work_trace, work_transition } from "./concord"

// Live probe: submit every published Concord tool schema to the Moonshot-backed
// opencode-go provider and fail when its flavored validator refuses any of them.
// Run: OPENCODE_API_KEY=... bun run adapter/opencode/moonshot_schema_probe.ts
// The endpoint and model are overridable for a staging bridge:
// OPENCODE_GO_BASE_URL (default https://opencode.ai/zen/go/v1),
// OPENCODE_GO_MODEL (default kimi-k2.7-code).

const baseURL = (process.env.OPENCODE_GO_BASE_URL ?? "https://opencode.ai/zen/go/v1").replace(/\/+$/, "")
const model = process.env.OPENCODE_GO_MODEL ?? "kimi-k2.7-code"
const apiKey = process.env.OPENCODE_API_KEY
if (!apiKey) fail("OPENCODE_API_KEY is not set; the probe authenticates as the opencode-go provider")

const coreTools: Record<string, any> = {
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

function publishedArgsSchema(args: Record<string, unknown>): Record<string, unknown> {
  const properties = Object.fromEntries(Object.entries(args).filter(([, value]) => value !== undefined))
  return { type: "object", properties, required: Object.keys(properties) }
}

const tools: Array<{ name: string; description: string; parameters: unknown }> = Object.entries(coreTools).map(([name, exportedTool]) => ({
  name,
  description: String(exportedTool.description ?? ""),
  parameters: publishedArgsSchema(exportedTool.args as Record<string, unknown>),
}))

const workStartDefinition = { description: String(work_start.description ?? ""), parameters: {} as unknown, jsonSchema: undefined as unknown }
await publishWorkStartDefinition({ toolID: "concord_work_start" }, workStartDefinition)
tools.push({ name: "concord_work_start", description: workStartDefinition.description, parameters: workStartDefinition.jsonSchema })

function fail(message: string): never {
  throw new Error(message)
}

async function probe(names: string[]): Promise<{ status: number; body: string }> {
  const response = await fetch(`${baseURL}/chat/completions`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${apiKey}`,
      "x-opencode-session": process.env.OPENCODE_GO_SESSION ?? "concord-moonshot-schema-probe",
    },
    body: JSON.stringify({
      model,
      messages: [{ role: "user", content: "Acknowledge." }],
      max_tokens: 8,
      tools: tools.filter((tool) => names.includes(tool.name)).map((tool) => ({ type: "function", function: { name: tool.name, description: tool.description, parameters: tool.parameters } })),
    }),
  })
  return { status: response.status, body: await response.text() }
}

function refusalDetail(body: string): string {
  try {
    const parsed = JSON.parse(body)
    return parsed?.error?.message ?? body
  } catch {
    return body
  }
}

const all = await probe(tools.map((tool) => tool.name))
if (all.status === 200) {
  console.log(`moonshot live probe passed: ${tools.length} tools accepted by ${model} (${baseURL})`)
  process.exit(0)
}

console.error(`moonshot live probe refused all ${tools.length} tools: HTTP ${all.status}: ${refusalDetail(all.body).slice(0, 600)}`)
let refused = 0
for (const tool of tools) {
  const single = await probe([tool.name])
  if (single.status !== 200) {
    refused += 1
    console.error(`refused: ${tool.name}: HTTP ${single.status}: ${refusalDetail(single.body).slice(0, 600)}`)
  }
}
if (refused === 0) fail(`the provider refused the combined request but accepted each tool alone: HTTP ${all.status}: ${refusalDetail(all.body).slice(0, 600)}`)
fail(`${refused} of ${tools.length} tools refused by ${model}`)
