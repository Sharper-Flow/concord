import { coreBinary } from "./generated-release"

export interface CoreRunnerResult { exitCode: number; stdout: string; stderr: string; signal_state?: string | null }
export interface CoreRunner { run(argv: string[], input: string, signal: AbortSignal): Promise<CoreRunnerResult> }

export const defaultRunner: CoreRunner = {
  async run(argv, input, signal) {
    const child = Bun.spawn(argv, { stdin: "pipe", stdout: "pipe", stderr: "pipe" })
    const abort = () => child.kill()
    if (signal.aborted) abort()
    signal.addEventListener("abort", abort, { once: true })
    await child.stdin.write(input)
    await child.stdin.end()
    const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    signal.removeEventListener("abort", abort)
    return { exitCode, stdout, stderr, signal_state: child.signalCode ?? null }
  },
}

let coreBinaryOverride: string | null = null
export function resolveCoreBinary(override: string | undefined, bound: string | null, stamped: string): string {
  return override ?? bound ?? stamped
}
export function concordBinaryPath(override?: string): string {
  const path = resolveCoreBinary(override, coreBinaryOverride, coreBinary)
  if (!path) throw new CoreBinaryUnavailable("this adapter copy is not bound to a release: generated-release.ts carries no core path, so no core call can run (CD-0111 D1)")
  return path
}
/** Test seam: bind the transport to an explicit core binary, or reset it. */
export function configureCoreBinary(path: string | null) { coreBinaryOverride = path }
export class CoreBinaryUnavailable extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}
function resolveSchemaRef(root: any, ref: string): any {
  if (typeof ref !== "string" || !ref.startsWith("#")) return undefined
  if (ref === "#") return root
  if (!ref.startsWith("#/")) return undefined
  let node = root
  for (const raw of ref.slice(2).split("/")) {
    if (!node || typeof node !== "object") return undefined
    node = node[raw.replace(/~1/g, "/").replace(/~0/g, "~")]
  }
  return node
}

// The adapter validates self-contained contracts at the transport boundary.
function validateSchema(schema: any, value: unknown, root: any, path = "", failures?: string[]): boolean {
  const fail = (reason: string): false => { failures?.push(path ? `${path}: ${reason}` : reason); return false }
  if (!schema || typeof schema !== "object") return fail("no schema to validate against")
  if (schema.$ref !== undefined) {
    const target = resolveSchemaRef(root, schema.$ref)
    if (!target || typeof target !== "object") return fail(`unresolvable $ref ${String(schema.$ref)}`)
    if (!validateSchema(target, value, root, path, failures)) return false
  }
  if (schema.const !== undefined && JSON.stringify(schema.const) !== JSON.stringify(value)) return fail(`must equal ${JSON.stringify(schema.const)}`)
  if (schema.enum !== undefined) {
    if (!Array.isArray(schema.enum)) return fail("enum keyword is not a list")
    const encoded = JSON.stringify(value)
    if (!schema.enum.some((member: unknown) => JSON.stringify(member) === encoded)) return fail(`is outside the closed enum; expected one of ${JSON.stringify(schema.enum)}`)
  }
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    const valid = types.some((type: string) => type === "object" ? isRecord(value) : type === "array" ? Array.isArray(value) : type === "integer" ? typeof value === "number" && Number.isInteger(value) : type === "number" ? typeof value === "number" : type === "null" ? value === null : typeof value === type)
    if (!valid) return fail(`is not of type ${types.join(" | ")}`)
  }
  if (typeof value === "string") {
    const length = [...value].length
    if (schema.minLength !== undefined && length < schema.minLength) return fail(`carries ${length} Unicode code points against a minimum of ${schema.minLength}`)
    if (schema.maxLength !== undefined && length > schema.maxLength) return fail(`carries ${length} Unicode code points against a limit of ${schema.maxLength}`)
    const bytes = Buffer.byteLength(value)
    if (schema["x-maxBytes"] !== undefined && bytes > schema["x-maxBytes"]) return fail(`carries ${bytes} UTF-8 bytes against a limit of ${schema["x-maxBytes"]}`)
    if (schema["x-minBytes"] !== undefined && bytes < schema["x-minBytes"]) return fail(`carries ${bytes} UTF-8 bytes against a minimum of ${schema["x-minBytes"]}`)
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) return fail(`does not match ${schema.pattern}`)
  }
  if (typeof value === "number") {
    if (schema.minimum !== undefined && value < schema.minimum) return fail(`is below the minimum ${schema.minimum}`)
    if (schema.maximum !== undefined && value > schema.maximum) return fail(`is above the maximum ${schema.maximum}`)
  }
  if (isRecord(value)) {
    const properties = schema.properties ?? {}
    const patterns = Object.entries(schema.patternProperties ?? {}).map(([pattern, child]) => ({ pattern: new RegExp(pattern), child }))
    const count = Object.keys(value).length
    if (schema.minProperties !== undefined && count < schema.minProperties) return fail(`carries ${count} properties against a minimum of ${schema.minProperties}`)
    if (schema.maxProperties !== undefined && count > schema.maxProperties) return fail(`carries ${count} properties against a limit of ${schema.maxProperties}`)
    const missing = (schema.required ?? []).filter((key: string) => !Object.hasOwn(value, key))
    if (missing.length > 0) return fail(`is missing required propert${missing.length === 1 ? "y" : "ies"} ${missing.join(", ")}`)
    for (const [key, child] of Object.entries(properties)) if (Object.hasOwn(value, key) && !validateSchema(child, value[key], root, path ? `${path}.${key}` : key, failures)) return false
    for (const { pattern, child } of patterns) for (const [key, entry] of Object.entries(value)) if (pattern.test(key) && !validateSchema(child, entry, root, path ? `${path}.${key}` : key, failures)) return false
    if (schema.additionalProperties === false) {
      const extra = Object.keys(value).filter(key => !Object.hasOwn(properties, key) && !patterns.some(({ pattern }) => pattern.test(key)))
      if (extra.length > 0) return fail(`carries undeclared propert${extra.length === 1 ? "y" : "ies"} ${extra.join(", ")}`)
    }
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems) return fail(`carries ${value.length} item(s) against a minimum of ${schema.minItems}`)
    if (schema.maxItems !== undefined && value.length > schema.maxItems) return fail(`carries ${value.length} item(s) against a limit of ${schema.maxItems}`)
    if (schema.uniqueItems && new Set(value.map(item => JSON.stringify(item))).size !== value.length) return fail("carries duplicate items")
    if (schema.items) for (let index = 0; index < value.length; index++) if (!validateSchema(schema.items, value[index], root, `${path}[${index}]`, failures)) return false
  }
  if (Array.isArray(schema.allOf)) for (const branch of schema.allOf) if (!validateSchema(branch, value, root, path, failures)) return false
  for (const keyword of ["oneOf", "anyOf"] as const) {
    if (!Array.isArray(schema[keyword])) continue
    const branchFailures: string[][] = []
    const matched: number[] = []
    for (const [index, branch] of schema[keyword].entries()) {
      const branchFailure: string[] = []
      if (validateSchema(branch, value, root, path, branchFailure)) matched.push(index)
      branchFailures.push(branchFailure)
    }
    if (keyword === "oneOf" ? matched.length !== 1 : matched.length < 1) {
      const reasons = branchFailures.flatMap((reasons_, index) => reasons_.map(reason => `branch ${index}: ${reason}`))
      return fail(`matches ${matched.length} ${keyword} branches${keyword === "oneOf" ? ` ([${matched.join(", ")}]); exactly one is required` : "; at least one is required"}${reasons.length > 0 ? `: ${reasons.slice(0, 8).join("; ")}` : ""}`)
    }
  }
  if (schema.if !== undefined) {
    const branch = validateSchema(schema.if, value, root, path) ? schema.then : schema.else
    if (branch !== undefined && branch !== null && !validateSchema(branch, value, root, path, failures)) return false
  }
  if (schema.not !== undefined && validateSchema(schema.not, value, root, path)) return fail("matches a forbidden schema")
  return true
}
export function validateAgainstSchema(schema: unknown, value: unknown, failures?: string[]): boolean {
  return validateSchema(schema, value, schema, "", failures)
}
