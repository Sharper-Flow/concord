// The host autoloads every tool-shaped named export of a TypeScript module
// under the custom tools directory (~/.config/opencode/tools) and publishes
// it as `<file>_<export>` beside the plugin registration. The plugin entry
// is therefore the only surface that may name a tool: a shipped module may
// export a tool shape only under a name the plugin's own registration
// carries, so the host never sees a second, foreign-named copy of a Concord
// tool.
import { afterAll, expect, test } from "bun:test"
import fs from "node:fs"
import path from "node:path"
import { pathToFileURL } from "node:url"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureCiWatch } from "./ci-watch"
import { manifestDigest } from "./generated-contracts"
import { configureHostLease } from "./host-lease"
import { hostControlPlane } from "./move-session"

const ADAPTER_DIR = import.meta.dir
const ENTRY_FILE = "concord-plugin.ts"
const RELEASE_ROOT = "/synthetic-release"

// The shipped set follows the entry module's relative import graph, the same
// walk scripts/install.py derives ADAPTER_FILES from, so no file list is
// restated here. scripts/test-installer.py pins the installer literal to
// this same graph.
function shippedAdapterFiles(): string[] {
  const shipped = new Set<string>()
  const pending = [ENTRY_FILE]
  while (pending.length > 0) {
    const name = pending.pop()!
    if (shipped.has(name)) continue
    const source = fs.readFileSync(path.join(ADAPTER_DIR, name), "utf-8")
    shipped.add(name)
    for (const match of source.matchAll(/(?:from\s+|import\(\s*)(["'])(\.[^"']+)\1/g)) {
      const target = match[2]
      if (target.startsWith("../")) throw new Error(`${name} imports outside the adapter directory at ${target}`)
      pending.push(`${target.slice(2)}.ts`)
    }
  }
  return [...shipped].sort()
}

function autoloadToolName(namespace: string, exportName: string): string {
  return exportName === "default" ? namespace : `${namespace}_${exportName}`
}

// The host admits an export as a custom tool when it is an object carrying
// `args`, a string `description`, and a function `execute`.
function isToolShaped(value: unknown): boolean {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false
  const candidate = value as { args?: unknown; description?: unknown; execute?: unknown }
  return (
    candidate.args !== undefined &&
    typeof candidate.args === "object" &&
    typeof candidate.description === "string" &&
    typeof candidate.execute === "function"
  )
}

// The registered names come from the plugin factory's own result, under the
// same mocked host-lease claim the adapter suite's other plugin tests use;
// no tool runs, so the client refuses every route it is handed.
async function registeredToolNames(): Promise<Set<string>> {
  const refusing = async () => {
    throw new Error("no tool executes in this test")
  }
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: RELEASE_ROOT },
    runner: {
      async run(argv: string[]) {
        if (argv[1] !== "host-lease") throw new Error(`unexpected core invocation: ${argv.join(" ")}`)
        return {
          exitCode: 0,
          stdout: JSON.stringify({
            pid: 4242,
            pid_start: 1,
            release_root: RELEASE_ROOT,
            core_binary: "concord",
            schema_version: 93,
            manifest_digest: manifestDigest,
          }),
          stderr: "",
        }
      },
    } as never,
  })
  const plugin = await ConcordAdapterPlugin({
    client: { _client: { get: refusing, post: refusing } } as never,
  })
  return new Set(Object.keys(plugin.tool))
}

test("every tool-shaped export of a shipped module autoloads only under a plugin-registered name", async () => {
  const registered = await registeredToolNames()
  expect(registered.size).toBeGreaterThan(0)
  for (const key of registered) expect(key.startsWith("concord_")).toBe(true)
  const files = shippedAdapterFiles()
  expect(files).toContain(ENTRY_FILE)
  expect(files).toContain("ci-watch.ts")
  const foreign: string[] = []
  for (const file of files) {
    const namespace = path.basename(file, ".ts")
    const mod = (await import(pathToFileURL(path.join(ADAPTER_DIR, file)).href)) as Record<string, unknown>
    for (const [exportName, value] of Object.entries(mod)) {
      if (!isToolShaped(value)) continue
      const autoloaded = autoloadToolName(namespace, exportName)
      if (!registered.has(autoloaded)) foreign.push(autoloaded)
    }
  }
  expect(foreign).toEqual([])
})

test("the watcher definition leaves ci-watch only through a non-tool shape", async () => {
  // `concord_ci_watch` is registered by the plugin alone; this module must
  // publish no tool-shaped binding, because the host would autoload it under
  // a foreign `ci-watch_*` name. The factory is a function export, which the
  // host does not treat as a tool.
  const mod = (await import("./ci-watch")) as Record<string, unknown>
  expect(Object.keys(mod).filter((name) => isToolShaped(mod[name]))).toEqual([])
  expect(typeof mod.ciWatchTool).toBe("function")
  const tool = (mod.ciWatchTool as () => unknown)()
  expect(isToolShaped(tool)).toBe(true)
})

afterAll(() => {
  hostControlPlane().bind(undefined)
  configureCiWatch({ reset: true })
  configureHostLease({ reset: true })
})
