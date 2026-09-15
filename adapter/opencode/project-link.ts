import { releaseRoot } from "./generated-release"
import fs from "node:fs"
import path from "node:path"
import { createHash } from "node:crypto"

const CONDUCT_ENTRY = path.join(path.dirname(releaseRoot), "current", "instructions", "*.md")
const PROJECT_LINK_OWNERSHIP = path.join(path.dirname(releaseRoot), "project-link-ownership.json")

type ProjectLinkRecord = {
  action: "remove" | "restore" | "preserve"
  scope: "project" | "worktree" | "legacy"
  expected: { exists: boolean; sha256?: string }
  original?: string
}

type ProjectLinkOwnership = { schema: 1; links: Record<string, ProjectLinkRecord> }

export class ProjectLinkError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = "ProjectLinkError"
    this.code = code
  }
}

function stripJSONC(text: string): string {
  let result = ""
  let string = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let index = 0; index < text.length; index++) {
    const character = text[index]
    const next = text[index + 1]
    if (comment === "line") {
      if (character === "\n") comment = ""
      continue
    }
    if (comment === "block") {
      if (character === "*" && next === "/") {
        comment = ""
        index++
      }
      continue
    }
    if (string) {
      result += character
      if (escaped) escaped = false
      else if (character === "\\") escaped = true
      else if (character === '"') string = false
      continue
    }
    if (character === '"') {
      string = true
      result += character
    } else if (character === "/" && next === "/") {
      comment = "line"
      index++
    } else if (character === "/" && next === "*") {
      comment = "block"
      index++
    } else {
      result += character
    }
  }
  return result.replace(/,(\s*[}\]])/g, "$1")
}

function parseProjectConfig(file: string, text: string): unknown {
  return file.endsWith(".json") ? JSON.parse(text) : JSON.parse(stripJSONC(text))
}

function validateProjectConfig(file: string, text: string): void {
  try {
    parseProjectConfig(file, text)
  } catch (error) {
    throw new Error(`cannot safely write worktree OpenCode config ${file}: ${String(error)}`)
  }
}

function jsoncObjectEnd(text: string): number {
  let depth = 0
  let inString = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let index = 0; index < text.length; index++) {
    const character = text[index]
    const next = text[index + 1]
    if (comment === "line") {
      if (character === "\n") comment = ""
      continue
    }
    if (comment === "block") {
      if (character === "*" && next === "/") {
        comment = ""
        index++
      }
      continue
    }
    if (!inString && character === "/" && next === "/") {
      comment = "line"
      index++
      continue
    }
    if (!inString && character === "/" && next === "*") {
      comment = "block"
      index++
      continue
    }
    if (inString) {
      if (escaped) escaped = false
      else if (character === "\\") escaped = true
      else if (character === '"') inString = false
      continue
    }
    if (character === '"') inString = true
    else if (character === "{") depth++
    else if (character === "}" && --depth === 0) return index
  }
  throw new Error("worktree OpenCode config has an unbalanced object")
}

function hasTrailingObjectComma(text: string): boolean {
  return /,(?:(?:[ \t]*\/\/[^\n]*(?:\n|$))|(?:[ \t]*\/\*.*?\*\/[ \t]*))*[ \t\r\n]*$/s.test(text)
}

function jsoncArrayEnd(text: string, key: string): number {
  const marker = JSON.stringify(key)
  const keyStart = text.indexOf(marker)
  if (keyStart < 0) throw new Error(`worktree OpenCode config has no ${key} array`)
  const arrayStart = text.indexOf("[", keyStart + marker.length)
  if (arrayStart < 0) throw new Error(`worktree OpenCode config has no ${key} array`)
  let depth = 0
  let inString = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let index = arrayStart; index < text.length; index++) {
    const character = text[index]
    const next = text[index + 1]
    if (comment === "line") {
      if (character === "\n") comment = ""
      continue
    }
    if (comment === "block") {
      if (character === "*" && next === "/") {
        comment = ""
        index++
      }
      continue
    }
    if (!inString && character === "/" && next === "/") {
      comment = "line"
      index++
      continue
    }
    if (!inString && character === "/" && next === "*") {
      comment = "block"
      index++
      continue
    }
    if (inString) {
      if (escaped) escaped = false
      else if (character === "\\") escaped = true
      else if (character === '"') inString = false
      continue
    }
    if (character === '"') inString = true
    else if (character === "[") depth++
    else if (character === "]" && --depth === 0) return index
  }
  throw new Error(`worktree OpenCode config has an unbalanced ${key} array`)
}

function appendInstructionJSONC(original: string, entry: string): string {
  const marker = JSON.stringify(entry)
  const parsed = JSON.parse(stripJSONC(original)) as Record<string, unknown>
  if (parsed.instructions === undefined) {
    const end = jsoncObjectEnd(original)
    const before = original.slice(0, end)
    const separator = before.trimEnd().endsWith("{") || hasTrailingObjectComma(before) ? "" : ","
    return original.slice(0, end) + `${separator}\n  "instructions": [\n    ${marker}\n  ]\n` + original.slice(end)
  }
  if (!Array.isArray(parsed.instructions) || !parsed.instructions.every((value) => typeof value === "string")) {
    throw new Error("worktree OpenCode config has a non-array instructions entry")
  }
  if (parsed.instructions.includes(entry)) return original
  const end = jsoncArrayEnd(original, "instructions")
  const before = original.slice(0, end)
  const arrayStart = before.lastIndexOf("[")
  const trailing = before.slice(arrayStart + 1)
  const hasTrailingComma = /,(?:(?:[ \t]*\/\/[^\n]*(?:\n|$))|(?:[ \t]*\/\*.*?\*\/[ \t]*))*[ \t\r\n]*$/s.test(trailing)
  const separator = hasTrailingComma ? "\n  " : "\n  ,\n  "
  const arraySeparator = parsed.instructions.length === 0 ? "\n  " : separator
  return before + arraySeparator + marker + "\n" + original.slice(end)
}

function managedWorktree(directory: string): boolean {
  const root = path.join(path.dirname(releaseRoot), "worktrees")
  const lexicalRoot = path.resolve(root)
  const lexicalDirectory = path.resolve(directory)
  const lexicalRelative = path.relative(lexicalRoot, lexicalDirectory)
  if (lexicalRelative === "" || lexicalRelative.startsWith("..") || path.isAbsolute(lexicalRelative)) return false
  let realRoot: string
  let realDirectory: string
  try {
    realRoot = fs.realpathSync(root)
    realDirectory = fs.realpathSync(directory)
  } catch (error) {
    throw new ProjectLinkError("managed_worktree_unreadable", `cannot resolve managed worktree ${directory}: ${String(error)}`)
  }
  const relative = path.relative(realRoot, realDirectory)
  if (relative === "" || relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new ProjectLinkError("managed_worktree_escape", `managed worktree ${directory} resolves outside the managed worktree root ${realRoot}`)
  }
  return true
}

function sha256(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex")
}

function readProjectLinkOwnership(): ProjectLinkOwnership {
  if (!fs.existsSync(PROJECT_LINK_OWNERSHIP)) return { schema: 1, links: {} }
  if (fs.lstatSync(PROJECT_LINK_OWNERSHIP).isSymbolicLink()) {
    throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  }
  let value: unknown
  try {
    value = JSON.parse(fs.readFileSync(PROJECT_LINK_OWNERSHIP, "utf8"))
  } catch (error) {
    throw new ProjectLinkError("ownership_invalid", `cannot read project link ownership record ${PROJECT_LINK_OWNERSHIP}: ${String(error)}`)
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new ProjectLinkError("ownership_invalid", `refusing malformed project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  const record = value as Record<string, unknown>
  if (record.schema !== 1 || record.links === null || typeof record.links !== "object" || Array.isArray(record.links)) throw new ProjectLinkError("ownership_invalid", `refusing malformed project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  const links = record.links as Record<string, unknown>
  if (Object.keys(links).length > 1024) throw new ProjectLinkError("ownership_invalid", `project link ownership record contains too many entries: ${PROJECT_LINK_OWNERSHIP}`)
  const validated: Record<string, ProjectLinkRecord> = {}
  for (const [file, raw] of Object.entries(links)) {
    if (path.resolve(file) !== file || path.normalize(file) !== file || raw === null || typeof raw !== "object" || Array.isArray(raw)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid project link ownership entry for ${file}`)
    }
    const candidate = raw as Record<string, unknown>
    if (Object.keys(candidate).some((key) => !["action", "scope", "expected", "original"].includes(key))) throw new ProjectLinkError("ownership_invalid", `refusing unknown project link ownership fields for ${file}`)
    const action = candidate.action
    const scope = candidate.scope
    const expected = candidate.expected
    if (!["remove", "restore", "preserve"].includes(String(action)) || !["project", "worktree", "legacy"].includes(String(scope)) || expected === null || typeof expected !== "object" || Array.isArray(expected)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid project link ownership entry for ${file}`)
    }
    const expectedRecord = expected as Record<string, unknown>
    if (Object.keys(expectedRecord).some((key) => key !== "exists" && key !== "sha256")) throw new ProjectLinkError("ownership_invalid", `refusing unknown expected project config fields for ${file}`)
    if (typeof expectedRecord.exists !== "boolean" || (expectedRecord.exists && (typeof expectedRecord.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(expectedRecord.sha256))) || (!expectedRecord.exists && expectedRecord.sha256 !== undefined)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid expected project config state for ${file}`)
    }
    if (action === "restore" && typeof candidate.original !== "string") throw new ProjectLinkError("ownership_invalid", `refusing missing original project config for ${file}`)
    if (action !== "restore" && candidate.original !== undefined) throw new ProjectLinkError("ownership_invalid", `refusing unexpected original project config for ${file}`)
    validated[file] = { action: action as ProjectLinkRecord["action"], scope: scope as ProjectLinkRecord["scope"], expected: expectedRecord as ProjectLinkRecord["expected"], ...(action === "restore" ? { original: candidate.original as string } : {}) }
  }
  return { schema: 1, links: validated }
}

function writeProjectLinkOwnership(ownership: ProjectLinkOwnership): void {
  const parent = path.dirname(PROJECT_LINK_OWNERSHIP)
  fs.mkdirSync(parent, { recursive: true })
  if (fs.existsSync(PROJECT_LINK_OWNERSHIP) && fs.lstatSync(PROJECT_LINK_OWNERSHIP).isSymbolicLink()) {
    throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  }
  const temporary = `${PROJECT_LINK_OWNERSHIP}.tmp-${process.pid}`
  try {
    fs.writeFileSync(temporary, JSON.stringify({ schema: 1, links: Object.fromEntries(Object.entries(ownership.links).sort(([left], [right]) => left.localeCompare(right))) }, null, 2) + "\n", { encoding: "utf8", flag: "wx" })
    fs.renameSync(temporary, PROJECT_LINK_OWNERSHIP)
  } catch (error) {
    try { fs.unlinkSync(temporary) } catch { /* best effort cleanup */ }
    throw new ProjectLinkError("ownership_write_failed", `cannot update project link ownership record ${PROJECT_LINK_OWNERSHIP}: ${String(error)}`)
  }
}

function currentProjectState(file: string): { exists: boolean; sha256?: string } {
  const stat = lstatIfPresent(file)
  if (!stat) return { exists: false }
  if (stat.isSymbolicLink() || !stat.isFile()) throw new ProjectLinkError("project_config_unsafe", `refusing non-file or symlinked worktree OpenCode config ${file}`)
  return { exists: true, sha256: sha256(fs.readFileSync(file, "utf8")) }
}

function ownershipAllowsRepair(file: string, expected: { exists: boolean; sha256?: string }): boolean {
  const actual = currentProjectState(file)
  if (!expected.exists) return !actual.exists
  return !actual.exists || actual.sha256 === expected.sha256
}

function recordProjectLink(file: string, original: string | null, updated: string, scope: "worktree"): void {
  const ownership = readProjectLinkOwnership()
  const key = path.resolve(file)
  const previous = ownership.links[key]
  if (previous && !ownershipAllowsRepair(file, previous.expected)) {
    throw new ProjectLinkError("project_config_modified", `refusing to adopt operator-modified worktree OpenCode config ${file}`)
  }
  const action = previous?.action === "remove" || previous?.action === "restore"
    ? previous.action
    : original === null ? "remove" : "restore"
  const next: ProjectLinkRecord = { action, scope, expected: { exists: true, sha256: sha256(updated) } }
  if (action === "restore") next.original = previous?.original ?? original ?? ""
  ownership.links[key] = next
  writeProjectLinkOwnership(ownership)
}

function lstatIfPresent(file: string): fs.Stats | null {
  try {
    return fs.lstatSync(file)
  } catch (error) {
    if (error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code === "ENOENT") return null
    throw error
  }
}

export async function ensureConductLink(directory: string, signal: AbortSignal): Promise<void> {
  if (signal.aborted || !managedWorktree(directory)) return
  const configDir = path.join(directory, ".opencode")
  const configDirStat = lstatIfPresent(configDir)
  if (configDirStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode directory ${configDir}`)
  fs.mkdirSync(configDir, { recursive: true })
  const json = path.join(configDir, "opencode.json")
  const jsonc = path.join(configDir, "opencode.jsonc")
  const jsonStat = lstatIfPresent(json)
  const jsoncStat = lstatIfPresent(jsonc)
  if (jsonStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode config ${json}`)
  if (jsoncStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode config ${jsonc}`)
  const target = jsonStat ? json : jsoncStat ? jsonc : json
  if (!lstatIfPresent(target)) {
    const created = JSON.stringify({ instructions: [CONDUCT_ENTRY] }, null, 2) + "\n"
    validateProjectConfig(target, created)
    await Bun.write(target, created)
    return
  }
  const original = await Bun.file(target).text()
  let parsed: unknown
  try {
    parsed = parseProjectConfig(target, original)
  } catch {
    throw new Error(`cannot parse worktree OpenCode config ${target}`)
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`worktree OpenCode config ${target} is not an object`)
  const updated = appendInstructionJSONC(original, CONDUCT_ENTRY)
  if (updated !== original) {
    validateProjectConfig(target, updated)
    recordProjectLink(target, original, updated, "worktree")
    await Bun.write(target, updated)
  }
}
