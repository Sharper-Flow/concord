// One plain-JavaScript ES module owns the worker report-protocol selector.
// The Bun adapter and Node ESM deterministic lane-report assertions import
// the same selector directly.
//
// Grammar. `concord-worker-result-v1` is the single reserved Markdown fence
// info string. Designated framing is exact: an opening fence of exactly three
// backticks at column 0 carrying the reserved info. Markdown quoting is
// char- and length-faithful: backtick and tilde fences (three or more of one
// character, info free of that character) close only on a run of the same
// character at least as long as the opening, so a three-backtick line never
// releases content from a four-backtick or tilde fence. A reserved info on
// any non-framing run (longer backtick run, tilde fence) quotes only: it is
// never a frame and never a legacy candidate. An unknown or partial reserved
// info on exactly three backticks is an announced unsupported_protocol
// error. LF and CRLF line endings are both recognized, and fence state is
// tracked per text part: a reserved marker quoted inside another fence is
// content, not a frame.
//
// Resolution order, evaluated after the UTF-8 input bound:
//   1. a fence info beginning with the reserved family prefix
//      `concord-worker-result` that is not the pinned protocol is an
//      announced unsupported_protocol error, in either mode, regardless of
//      what else the output carries (no fallback ever runs);
//   2. more than one pinned designated frame is ambiguous, even when the
//      frame bytes are identical;
//   3. exactly one designated frame is validated alone: an unclosed fence,
//      non-strict JSON, duplicate keys, or a non-object body is malformed,
//      and framing success selects without consulting isReport (framing
//      grants no content admission; the closed schema runs downstream);
//   4. with no designated frame, a dispatch that pins the protocol (the
//      `protocol` option) gets absent — legacy output never rescues a
//      current dispatch;
//   5. a dispatch without a protocol pin (historical packets) enumerates
//      complete strict JSON documents: the whole part when it stands alone,
//      ordinary fence bodies, and objects embedded in prose that a
//      balanced, string-aware walk recognizes as complete. Broken
//      substrings are never repaired, and an object nested inside an
//      already-parsed document is not excavated as a second candidate.
//
// Legacy rules: a candidate is report-shaped when isReport accepts it or,
// when isReport rejects or is absent, when it carries a worker-owned report
// field (status, evidence, readback_model, schema_version). One shaped
// candidate selects — including an invalid one, so the closed schema can
// report the actual violation. Two distinct shaped candidates are ambiguous
// even when equal; when isReport is supplied and the candidates mix valid
// and invalid shapes, the mix is malformed. A malformed announcement
// (strict-parse failure or duplicate keys in an announced document) after
// the last shaped candidate is malformed; before it, the later candidate
// wins. Absence stays distinct from malformation. A whole part that stands
// as one complete valid JSON document of another type is a diagnostic, not
// an excavation site: no candidate is taken from inside it.
//
// The selector is bounded and iterative: no recursion anywhere, one pass
// per scan, and inputs are never mutated. Artifact byte offsets and digests
// are computed over exact UTF-8 bytes.

import { createHash } from "node:crypto"

/** The single reserved report protocol fence info string. */
export const WORKER_REPORT_PROTOCOL = "concord-worker-result-v1"

const PROTOCOL_FAMILY_PREFIX = "concord-worker-result"
const DEFAULT_MAX_REPORT_BYTES = 65536
const REPORT_OWN_FIELDS = ["status", "evidence", "readback_model", "schema_version"]
const DETAIL_MESSAGE_CHARS = 120
const DETAIL_TAIL_CHARS = 120
const DETAIL_KEY_CHARS = 60
const DETAIL_OFFSET_ENTRIES = 4

/**
 * A selected report with the exact bytes it came from.
 *
 * @typedef {object} WorkerReportArtifact
 * @property {number} part_index   Index of the text part carrying the report.
 * @property {number} start_byte   UTF-8 byte offset of the artifact inside its part.
 * @property {number} end_byte     UTF-8 byte offset one past the artifact's last byte.
 * @property {string} text         The exact artifact text, original line endings preserved.
 * @property {string} sha256       SHA-256 hex digest of the artifact's UTF-8 bytes.
 *
 * @typedef {object} SelectedWorkerReport
 * @property {"selected"} kind
 * @property {Record<string, unknown>} report
 * @property {WorkerReportArtifact} artifact
 *
 * @typedef {object} RefusedWorkerReport
 * @property {"absent"|"malformed"|"ambiguous"|"unsupported_protocol"|"over_bound"} kind
 * @property {string} detail
 *
 * The discriminated result of report selection.
 * @typedef {SelectedWorkerReport|RefusedWorkerReport} WorkerReportSelection
 *
 * Schema-aware shape test over one parsed candidate object. The selector
 * treats a thrown error as a refusal (invalid shape), never a crash.
 * @typedef {(value: Record<string, unknown>) => boolean} WorkerReportShape
 *
 * @typedef {object} SelectWorkerReportOptions
 * @property {string|null} [protocol]  The dispatch-pinned protocol. The pinned
 *   value selects designated-frame mode; undefined or null selects the
 *   historical legacy path; any other string is an immediate
 *   unsupported_protocol refusal.
 * @property {number} [maxBytes]  Bound on the summed UTF-8 byte length of all
 *   text parts. Defaults to 65536. Checked before any parsing.
 * @property {WorkerReportShape} [isReport]  Closed-shape test consulted only
 *   on the legacy path.
 */

/** @returns {value is Record<string, unknown>} */
function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function isJsonWhitespace(code) {
  return code === 0x20 || code === 0x09 || code === 0x0a || code === 0x0d
}

// byteOffsetTable maps every UTF-16 index in text to the UTF-8 byte offset of
// that code unit, so artifact ranges stay byte exact around multibyte and
// astral characters. Surrogate pairs are 4 bytes; a lone surrogate encodes
// as the 3-byte replacement character, matching Buffer.from.
function byteOffsetTable(text) {
  const n = text.length
  const table = new Uint32Array(n + 1)
  let bytes = 0
  for (let i = 0; i < n; i++) {
    table[i] = bytes
    const code = text.charCodeAt(i)
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < n) {
      const next = text.charCodeAt(i + 1)
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4
        table[i + 1] = bytes
        i++
        continue
      }
    }
    if (code < 0x80) bytes += 1
    else if (code < 0x800) bytes += 2
    else bytes += 3
  }
  table[n] = bytes
  return table
}

function sha256Hex(text) {
  return createHash("sha256").update(text, "utf8").digest("hex")
}

function boundedKey(key) {
  return key.length > DETAIL_KEY_CHARS ? key.slice(0, DETAIL_KEY_CHARS) + "..." : key
}

// parseFailureReason renders a bounded why-not for a strict-parse failure:
// the parser's own message plus a flattened tail, mirroring the admission
// diagnostics the adapter already prints.
function parseFailureReason(text, error) {
  const message = (error instanceof Error ? error.message : String(error)).slice(0, DETAIL_MESSAGE_CHARS)
  const flat = text.replace(/\s+/g, " ")
  const tail = flat.length > DETAIL_TAIL_CHARS ? "..." + flat.slice(-DETAIL_TAIL_CHARS) : flat
  return `${message}; ${text.length} chars; ends with: ${tail}`
}

function skipJsonWhitespace(text, i) {
  const n = text.length
  while (i < n && isJsonWhitespace(text.charCodeAt(i))) i++
  return i
}

// decodeStringAt decodes the JSON string starting at text[i] === '"'. Only
// called on text that already passed JSON.parse, so escapes are well formed.
// Returns the decoded key and the index just past the closing quote.
function decodeStringAt(text, i) {
  const n = text.length
  let value = ""
  let j = i + 1
  while (j < n) {
    const c = text[j]
    if (c === '"') return { value, end: j + 1 }
    if (c === "\\") {
      const e = text[j + 1]
      if (e === "u") {
        value += String.fromCharCode(parseInt(text.slice(j + 2, j + 6), 16))
        j += 6
      } else {
        value += e === "b" ? "\b" : e === "f" ? "\f" : e === "n" ? "\n" : e === "r" ? "\r" : e === "t" ? "\t" : e
        j += 2
      }
      continue
    }
    value += c
    j++
  }
  return { value, end: j }
}

// skipString returns the index just past the closing quote of the JSON
// string at text[i], without decoding it.
function skipString(text, i) {
  const n = text.length
  let j = i + 1
  while (j < n) {
    const c = text[j]
    if (c === '"') return j + 1
    j += c === "\\" ? 2 : 1
  }
  return j
}

// firstDuplicateKey walks one JSON document iteratively — explicit stacks,
// no recursion — and returns the first duplicate object key it meets, with
// escape-decoded comparison so `"status"` and `"statu\u0073"` collide. JSON
// and JSON.parse are both last-key-wins; this scan refuses that instead.
// Returns null when every key is unique, and { error: true } only if the
// structure disagrees with a document JSON.parse already accepted.
function firstDuplicateKey(text) {
  const n = text.length
  /** @type {{keys: Set<string>|null}[]} */
  const stack = []
  let mode = "value"
  let i = skipJsonWhitespace(text, 0)
  while (i <= n) {
    if (i >= n) return mode === "end" ? null : { error: true }
    const c = text[i]
    const afterValue = () => {
      mode = stack.length === 0 ? "end" : "comma"
    }
    if (mode === "value") {
      if (c === "{") {
        stack.push({ keys: new Set() })
        i = skipJsonWhitespace(text, i + 1)
        // The close branch consumes the bracket at i, so an immediately
        // empty object leaves i parked on "}".
        mode = text[i] === "}" ? "close" : "object-key"
      } else if (c === "[") {
        stack.push({ keys: null })
        i = skipJsonWhitespace(text, i + 1)
        if (text[i] === "]") mode = "close"
        else continue
      } else if (c === '"') {
        i = skipString(text, i)
        afterValue()
      } else {
        while (i < n && !isJsonWhitespace(text.charCodeAt(i)) && text[i] !== "," && text[i] !== "]" && text[i] !== "}") i++
        afterValue()
      }
    } else if (mode === "object-key") {
      if (c !== '"') return { error: true }
      const key = decodeStringAt(text, i)
      const top = stack[stack.length - 1]
      if (top.keys.has(key.value)) return { duplicate: key.value }
      top.keys.add(key.value)
      i = skipJsonWhitespace(text, key.end)
      if (text[i] !== ":") return { error: true }
      mode = "value"
      i++
    } else if (mode === "comma") {
      const top = stack[stack.length - 1]
      if (c === ",") {
        mode = top.keys === null ? "value" : "object-key"
        i++
      } else if (c === "]" || c === "}") {
        // The closing bracket must disagree with its container's kind to be
        // a structural error: "]" closes arrays, "}" closes objects.
        if ((c === "]" ? 1 : 0) !== (top.keys === null ? 1 : 0)) return { error: true }
        mode = "close"
      } else return { error: true }
    } else if (mode === "close") {
      const top = stack[stack.length - 1]
      if (top === undefined) return { error: true }
      stack.pop()
      i++
      i = skipJsonWhitespace(text, i)
      mode = stack.length === 0 ? "end" : "comma"
      if (mode === "end" && i < n) return { error: true }
      if (mode === "comma") continue
      return null
    } else if (mode === "end") {
      i = skipJsonWhitespace(text, i)
      return i < n ? { error: true } : null
    }
    if (mode !== "end") i = skipJsonWhitespace(text, i)
  }
  return { error: true }
}

// parseStrictObject parses one candidate document: JSON.parse owns JSON
// syntax; the lexical scan owns duplicate-key refusal.
function parseStrictObject(text) {
  let value
  try {
    value = JSON.parse(text)
  } catch (error) {
    return { ok: false, kind: "syntax", reason: parseFailureReason(text, error) }
  }
  if (!isPlainObject(value)) {
    const typeName = Array.isArray(value) ? "array" : value === null ? "null" : typeof value
    return { ok: false, kind: "type", typeName }
  }
  const dup = firstDuplicateKey(text)
  if (dup !== null) {
    return dup.error
      ? { ok: false, kind: "syntax", reason: "the duplicate-key scan did not reproduce the parsed structure" }
      : { ok: false, kind: "duplicate", key: dup.duplicate }
  }
  return { ok: true, value }
}

function lineContent(text, start, end) {
  if (end > start && text[end - 1] === "\r") return text.slice(start, end - 1)
  return text.slice(start, end)
}

function leadingRun(line, char) {
  let n = 0
  while (n < line.length && line[n] === char) n++
  return n
}

// fenceLine recognizes one Markdown fence-delimiter line at column 0: a run
// of three or more backticks or tildes whose remainder carries no repeat of
// that delimiter character. Returns the delimiter char, run length, and
// info string, or null for an ordinary line.
function fenceLine(line) {
  const backticks = leadingRun(line, "`")
  if (backticks >= 3 && !line.slice(backticks).includes("`")) {
    return { char: "`", run: backticks, info: line.slice(backticks).trim() }
  }
  const tildes = leadingRun(line, "~")
  if (tildes >= 3 && !line.slice(tildes).includes("~")) {
    return { char: "~", run: tildes, info: line.slice(tildes).trim() }
  }
  return null
}

// A closing delimiter is a run of the opening's own character at least as
// long as the opening run, with nothing but trailing spaces after it. The
// other delimiter character never closes a fence, and a shorter run of the
// same character is content, so a three-backtick line cannot release
// content quoted by a four-backtick or tilde fence.
function isClosingFenceLine(line, char, minRun) {
  const run = leadingRun(line, char)
  return run >= minRun && line.slice(run).trim() === ""
}

// scanFences walks one text part line by line. Fence spans never overlap and
// the scan jumps past each closed fence, so the returned prose regions cover
// exactly the text outside fences.
function scanFences(text) {
  const n = text.length
  const fences = []
  const prose = []
  let proseStart = 0
  let pendingProse = true
  let i = 0
  while (i < n) {
    const lineEnd = text.indexOf("\n", i)
    const end = lineEnd === -1 ? n : lineEnd
    const line = lineContent(text, i, end)
    const open = fenceLine(line)
    if (open === null) {
      i = lineEnd === -1 ? n : lineEnd + 1
      continue
    }
    if (proseStart < i) prose.push([proseStart, i])
    const info = open.info
    const bodyStart = end + 1 <= n ? end + 1 : n
    let closed = false
    let closeLineStart = -1
    let closeContentEnd = -1
    let j = bodyStart
    while (j < n) {
      const closeEnd = text.indexOf("\n", j)
      const closeEndOrN = closeEnd === -1 ? n : closeEnd
      const closeLine = lineContent(text, j, closeEndOrN)
      if (isClosingFenceLine(closeLine, open.char, open.run)) {
        closed = true
        closeLineStart = j
        closeContentEnd = j + closeLine.length
        break
      }
      if (closeEnd === -1) break
      j = closeEnd + 1
    }
    if (closed) {
      fences.push({ char: open.char, run: open.run, info, lineStart: i, bodyStart, bodyEnd: closeLineStart, frameEnd: closeContentEnd, closed: true })
      const afterClose = text.indexOf("\n", closeContentEnd)
      i = afterClose === -1 ? n : afterClose + 1
      proseStart = i
      if (i >= n) pendingProse = proseStart < n
    } else {
      fences.push({ char: open.char, run: open.run, info, lineStart: i, bodyStart, bodyEnd: n, frameEnd: n, closed: false })
      i = n
      pendingProse = false
    }
  }
  if (pendingProse && proseStart < n) prose.push([proseStart, n])
  return { fences, prose }
}

function jsonTrimRange(text, start, end) {
  let s = start
  let e = end
  while (s < e && isJsonWhitespace(text.charCodeAt(s))) s++
  while (e > s && isJsonWhitespace(text.charCodeAt(e - 1))) e--
  return [s, e]
}

function containedInRange(start, end, ranges) {
  for (const [s, e] of ranges) if (start >= s && end <= e) return true
  return false
}

// scanInlineObjects records every complete object a balanced, string-aware
// walk recognizes in one prose region. It never descends into an object it
// already recorded, never repairs an unclosed one, and skips objects inside
// a range already parsed as a complete document (no nested excavation).
function scanInlineObjects(text, regionStart, regionEnd, part, offsets, out, parsedRanges) {
  let i = regionStart
  while (i < regionEnd) {
    const open = text.indexOf("{", i)
    if (open === -1 || open >= regionEnd) return
    let j = open + 1
    let depth = 1
    let inString = false
    let complete = false
    while (j < regionEnd) {
      const c = text[j]
      if (inString) {
        if (c === "\\") j += 2
        else {
          if (c === '"') inString = false
          j++
        }
        continue
      }
      if (c === '"') {
        inString = true
        j++
        continue
      }
      if (c === "{") depth++
      else if (c === "}") {
        depth--
        if (depth === 0) {
          complete = true
          j++
          break
        }
      }
      j++
    }
    if (!complete) return
    if (!containedInRange(open, j, parsedRanges)) {
      const sub = text.slice(open, j)
      const parse = parseStrictObject(sub)
      out.push({ part, start: open, end: j, announced: false, parse, offsets })
      if (parse.ok) parsedRanges.push([open, j])
    }
    i = j
  }
}

function hasOwnReportField(value) {
  return REPORT_OWN_FIELDS.some((field) => Object.prototype.hasOwnProperty.call(value, field))
}

function offsetList(entries) {
  const shown = entries.slice(0, DETAIL_OFFSET_ENTRIES).map((e) => `${e.part}@${e.offset}`).join(", ")
  return entries.length > DETAIL_OFFSET_ENTRIES ? shown + ", ..." : shown
}

/**
 * Select the worker's final report from ordered host text parts.
 *
 * @param {string[]} texts
 * @param {SelectWorkerReportOptions} [options]
 * @returns {WorkerReportSelection}
 */
export function selectWorkerReport(texts, options) {
  if (!Array.isArray(texts)) throw new TypeError("selectWorkerReport expects an array of text parts")
  for (const text of texts) {
    if (typeof text !== "string") throw new TypeError("selectWorkerReport expects every text part to be a string")
  }
  if (options !== undefined && options !== null && (typeof options !== "object" || Array.isArray(options))) {
    throw new TypeError("selectWorkerReport expects an options object")
  }
  const opts = options ?? {}
  if (opts.maxBytes !== undefined && (typeof opts.maxBytes !== "number" || !Number.isFinite(opts.maxBytes) || opts.maxBytes <= 0)) {
    throw new TypeError("selectWorkerReport expects maxBytes to be a positive finite number")
  }
  if (opts.protocol !== undefined && opts.protocol !== null && typeof opts.protocol !== "string") {
    throw new TypeError("selectWorkerReport expects protocol to be a string or null")
  }
  if (opts.isReport !== undefined && typeof opts.isReport !== "function") {
    throw new TypeError("selectWorkerReport expects isReport to be a function")
  }
  const maxBytes = opts.maxBytes ?? DEFAULT_MAX_REPORT_BYTES
  const isReport = typeof opts.isReport === "function" ? opts.isReport : null

  // The bound covers the exact summed UTF-8 bytes of every part and is
  // checked before any parsing: oversized output is refused whole, never
  // partially admitted or claimed fully copied.
  let totalBytes = 0
  for (const text of texts) totalBytes += Buffer.byteLength(text, "utf8")
  if (totalBytes > maxBytes) {
    return {
      kind: "over_bound",
      detail: `over-bound worker output: ${totalBytes} UTF-8 bytes across ${texts.length} text parts, over the ${maxBytes}-byte report bound; nothing was admitted or claimed fully copied`,
    }
  }

  if (opts.protocol !== undefined && opts.protocol !== null && opts.protocol !== WORKER_REPORT_PROTOCOL) {
    return {
      kind: "unsupported_protocol",
      detail: `unsupported report protocol: dispatch pinned "${opts.protocol}", and this selector implements only ${WORKER_REPORT_PROTOCOL}`,
    }
  }

  const frames = []
  const extractions = []
  for (let part = 0; part < texts.length; part++) {
    const text = texts[part]
    const offsets = byteOffsetTable(text)
    const { fences, prose } = scanFences(text)
    // Ranges that already hold one complete JSON document — a parsed object
    // or a whole valid document of another type. Nothing inside them is a
    // separate candidate.
    const documentRanges = []
    const recordDoc = (start, end, announced) => {
      if (start >= end || text.charCodeAt(start) !== 0x7b) return
      const sub = text.slice(start, end)
      const parse = parseStrictObject(sub)
      extractions.push({ part, start, end, announced, parse, offsets })
      if (parse.ok) documentRanges.push([start, end])
    }
    for (const fence of fences) {
      const reserved = fence.info.startsWith(PROTOCOL_FAMILY_PREFIX)
      const framing = fence.char === "`" && fence.run === 3
      // Designated framing is exactly three backticks. A reserved info on
      // any other run quotes only: no frame, no legacy candidate.
      const kind = framing
        ? fence.info === WORKER_REPORT_PROTOCOL ? "designated" : reserved ? "foreign" : "ordinary"
        : reserved ? "quoting" : "ordinary"
      frames.push({
        part,
        kind,
        info: fence.info,
        lineStart: fence.lineStart,
        bodyStart: fence.bodyStart,
        bodyEnd: fence.bodyEnd,
        frameEnd: fence.frameEnd,
        closed: fence.closed,
        offsets,
      })
      if (kind !== "ordinary") continue
      const [bodyStart, bodyEnd] = jsonTrimRange(text, fence.bodyStart, fence.bodyEnd)
      recordDoc(bodyStart, bodyEnd, true)
    }
    const [wholeStart, wholeEnd] = jsonTrimRange(text, 0, text.length)
    if (wholeStart < wholeEnd && text.charCodeAt(wholeStart) === 0x7b) {
      recordDoc(wholeStart, wholeEnd, true)
    } else if (wholeStart < wholeEnd) {
      // A whole part that stands as one complete valid JSON document of
      // another type is a diagnostic: the document owns its bytes, and no
      // candidate is excavated from inside it.
      const whole = text.slice(wholeStart, wholeEnd)
      let value
      try {
        value = JSON.parse(whole)
      } catch {
        value = undefined
      }
      if (value !== undefined) {
        const dup = firstDuplicateKey(whole)
        if (dup !== null && !dup.error) {
          extractions.push({ part, start: wholeStart, end: wholeEnd, announced: true, parse: { ok: false, kind: "duplicate", key: dup.duplicate }, offsets })
        } else {
          documentRanges.push([wholeStart, wholeEnd])
        }
      }
    }
    for (const [regionStart, regionEnd] of prose) scanInlineObjects(text, regionStart, regionEnd, part, offsets, extractions, documentRanges)
  }

  const foreign = frames.find((frame) => frame.kind === "foreign")
  if (foreign !== undefined) {
    return {
      kind: "unsupported_protocol",
      detail: `unsupported report protocol: worker output announced fence info "${foreign.info}", and this selector implements only ${WORKER_REPORT_PROTOCOL}; no fallback selection runs`,
    }
  }

  const designated = frames.filter((frame) => frame.kind === "designated")
  if (designated.length > 1) {
    const offsets = designated.map((frame) => ({ part: frame.part, offset: frame.offsets[frame.lineStart] }))
    return {
      kind: "ambiguous",
      detail: `ambiguous report selection: ${designated.length} designated ${WORKER_REPORT_PROTOCOL} frames (part@byte: ${offsetList(offsets)}); multiple final reports refuse selection even when their bytes match`,
    }
  }
  if (designated.length === 1) {
    const frame = designated[0]
    const text = texts[frame.part]
    if (!frame.closed) {
      return {
        kind: "malformed",
        detail: `malformed designated report frame: ${WORKER_REPORT_PROTOCOL} frame opened at part ${frame.part} byte ${frame.offsets[frame.lineStart]} never closed with a fence line`,
      }
    }
    const body = text.slice(frame.bodyStart, frame.bodyEnd)
    const parse = parseStrictObject(body)
    if (!parse.ok) {
      if (parse.kind === "duplicate") {
        return { kind: "malformed", detail: `malformed designated report frame: body repeats JSON object key "${boundedKey(parse.key)}"; duplicate keys refuse last-key-wins admission` }
      }
      if (parse.kind === "type") {
        return { kind: "malformed", detail: `malformed designated report frame: body parsed to ${parse.typeName}, not a JSON object` }
      }
      return { kind: "malformed", detail: `malformed designated report frame: body is not strict JSON: ${parse.reason}` }
    }
    const frameText = text.slice(frame.lineStart, frame.frameEnd)
    return {
      kind: "selected",
      report: parse.value,
      artifact: {
        part_index: frame.part,
        start_byte: frame.offsets[frame.lineStart],
        end_byte: frame.offsets[frame.frameEnd],
        text: frameText,
        sha256: sha256Hex(frameText),
      },
    }
  }

  if (opts.protocol !== undefined && opts.protocol !== null) {
    return { kind: "absent", detail: `absent report: worker output carried no designated ${WORKER_REPORT_PROTOCOL} frame` }
  }

  // Historical path: enumerate complete report-shaped documents.
  const shaped = []
  let lastPoison = null
  for (const extraction of extractions) {
    if (extraction.parse.ok) {
      const value = extraction.parse.value
      const ownShaped = hasOwnReportField(value)
      let accepted = false
      if (isReport !== null) {
        try {
          accepted = isReport(value) === true
        } catch {
          accepted = false
        }
      }
      // Without isReport the own report fields are the only shape test, so
      // an arbitrary JSON object never selects by default.
      if (!ownShaped && !accepted) continue
      shaped.push({ extraction, value, valid: accepted })
      continue
    }
    const announcedFailure = extraction.announced || extraction.parse.kind === "duplicate"
    if (!announcedFailure) continue
    if (lastPoison === null || extraction.part > lastPoison.part || (extraction.part === lastPoison.part && extraction.start > lastPoison.start)) {
      lastPoison = extraction
    }
  }

  if (shaped.length > 1) {
    const anyValid = shaped.some((entry) => entry.valid)
    const anyInvalid = shaped.some((entry) => !entry.valid)
    if (isReport !== null && anyValid && anyInvalid) {
      const firstInvalid = shaped.find((entry) => !entry.valid)
      return {
        kind: "malformed",
        detail: `malformed legacy report selection: worker output carried both valid and invalid report-shaped documents (first invalid at part ${firstInvalid.extraction.part} byte ${firstInvalid.extraction.offsets[firstInvalid.extraction.start]}); an invalid candidate refuses selection beside a valid one`,
      }
    }
    const offsets = shaped.map((entry) => ({ part: entry.extraction.part, offset: entry.extraction.offsets[entry.extraction.start] }))
    return {
      kind: "ambiguous",
      detail: `ambiguous report selection: ${shaped.length} complete report-shaped JSON documents (part@byte: ${offsetList(offsets)}); two distinct candidates refuse selection even when their reports are equal`,
    }
  }
  if (shaped.length === 1) {
    const entry = shaped[0]
    const position = { part: entry.extraction.part, start: entry.extraction.start }
    if (lastPoison !== null && (lastPoison.part > position.part || (lastPoison.part === position.part && lastPoison.start > position.start))) {
      return {
        kind: "malformed",
        detail: `malformed legacy report selection: an announced document at part ${lastPoison.part} byte ${lastPoison.offsets[lastPoison.start]} is not strict JSON${lastPoison.parse.kind === "duplicate" ? " (duplicate keys)" : ""}: ${lastPoison.parse.kind === "duplicate" ? "duplicate keys refuse last-key-wins admission" : lastPoison.parse.reason}`,
      }
    }
    const sub = texts[entry.extraction.part].slice(entry.extraction.start, entry.extraction.end)
    return {
      kind: "selected",
      report: entry.value,
      artifact: {
        part_index: entry.extraction.part,
        start_byte: entry.extraction.offsets[entry.extraction.start],
        end_byte: entry.extraction.offsets[entry.extraction.end],
        text: sub,
        sha256: sha256Hex(sub),
      },
    }
  }
  if (lastPoison !== null) {
    return {
      kind: "malformed",
      detail: `malformed legacy report selection: worker output announced a document that is not strict JSON (${lastPoison.parse.kind === "duplicate" ? "duplicate object keys refuse last-key-wins admission" : lastPoison.parse.reason})`,
    }
  }
  return { kind: "absent", detail: "absent report: worker output carried no complete report-shaped JSON document" }
}

const STREAM_SNIPPET_CHARS = 120
export const RUN_EVENT_TYPES = new Set(["step_start", "step_finish", "text", "reasoning", "tool_use", "error"])

// boundedSnippet flattens and bounds one stream fragment for a refusal
// detail, so a broken line can be recognized without copying the transcript.
function boundedSnippet(value) {
  const flat = String(value).replace(/\s+/g, " ")
  return flat.length > STREAM_SNIPPET_CHARS ? flat.slice(0, STREAM_SNIPPET_CHARS) + "..." : flat
}

/**
 * Collect the report-channel text parts from one host run stream: one
 * official JSON event per stdout line, already reassembled by the caller.
 * Only `text` events carrying a `text` part contribute; tool, reasoning,
 * and status JSON never enters the report channel.
 *
 * Under the current protocol pin (options.protocol ===
 * WORKER_REPORT_PROTOCOL) collection is strict: every event carries one
 * shared session identity, every text part carries a string id whose
 * part.sessionID — when present — agrees with its event, and the stream
 * must end on a terminal step_finish with reason "stop" (the last finish
 * decides). Parts are reconciled per (session, part.id): an exact replay
 * collects once, a snapshot that grows an earlier prefix replaces it in
 * place, and a same-id replacement with different, non-extending bytes
 * refuses. Without a pin — historical fixtures — ids, session identities,
 * and the terminal signal are not required, and id-less text parts collect
 * as-is; well-formedness of event lines is still enforced. A pin naming any
 * other protocol refuses immediately. part.messageID, when the host carries
 * it, is not part of the reconciliation identity.
 *
 * @param {string} stdout
 * @param {{protocol?: string|null}} [options]
 * @returns {{texts: string[], detail?: string}} The ordered text parts, or
 *   no texts plus a bounded detail when the stream refuses collection.
 */
export function readWorkerReportTexts(stdout, options) {
  if (typeof stdout !== "string") throw new TypeError("readWorkerReportTexts expects a string stream")
  if (options !== undefined && options !== null && (typeof options !== "object" || Array.isArray(options))) {
    throw new TypeError("readWorkerReportTexts expects an options object")
  }
  const opts = options ?? {}
  if (opts.protocol !== undefined && opts.protocol !== null && typeof opts.protocol !== "string") {
    throw new TypeError("readWorkerReportTexts expects protocol to be a string or null")
  }
  const pinned = opts.protocol === WORKER_REPORT_PROTOCOL
  if (opts.protocol !== undefined && opts.protocol !== null && !pinned) {
    return { texts: [], detail: `unsupported report protocol: dispatch pinned "${opts.protocol}", and this selector implements only ${WORKER_REPORT_PROTOCOL}` }
  }
  const refuse = (detail) => ({ texts: [], detail })

  const order = [] // part keys, plus {text} entries for id-less historical parts
  const parts = new Map() // (session, part.id) -> reconciled text
  let session = null
  let lastFinishReason = null

  for (const raw of stdout.split("\n")) {
    const line = raw.trim()
    // Host plugins can write logs and status objects beside official events.
    if (!line.startsWith("{")) continue
    let event
    try {
      event = JSON.parse(line)
    } catch {
      return refuse(`malformed official event data: a stream line is not JSON: ${boundedSnippet(line)}`)
    }
    if (!isPlainObject(event) || !RUN_EVENT_TYPES.has(event.type)) continue
    if (!isPlainObject(event.part) || typeof event.part.type !== "string") {
      return refuse(`malformed official event data: ${boundedSnippet(line)}`)
    }
    if (pinned && typeof event.sessionID !== "string") {
      return refuse(`malformed official stream: an event carries no session identity: ${boundedSnippet(line)}`)
    }
    if (typeof event.sessionID === "string") {
      if (session === null) session = event.sessionID
      else if (pinned && session !== event.sessionID) {
        return refuse(`malformed official stream: events carry more than one session identity (${boundedSnippet(session)}, ${boundedSnippet(event.sessionID)})`)
      }
    }
    const part = event.part
    if (event.type === "step_finish") {
      lastFinishReason = typeof part.reason === "string" ? part.reason : null
    }
    if (event.type !== "text" || part.type !== "text") continue
    if (typeof part.text !== "string") {
      if (!pinned) continue
      return refuse(`malformed official event data: a text part carries no string text: ${boundedSnippet(line)}`)
    }
    if (typeof part.id !== "string" || part.id === "") {
      if (pinned) return refuse(`malformed official stream: a text part carries no part id: ${boundedSnippet(line)}`)
      order.push({ text: part.text })
      continue
    }
    if (pinned && typeof part.sessionID === "string" && part.sessionID !== event.sessionID) {
      return refuse(`malformed official stream: text part ${boundedSnippet(part.id)} declares session ${boundedSnippet(part.sessionID)} inside an event of session ${boundedSnippet(event.sessionID)}`)
    }
    const key = (typeof event.sessionID === "string" ? event.sessionID + "\u0000" : "") + part.id
    const existing = parts.get(key)
    if (existing === undefined) {
      parts.set(key, part.text)
      order.push(key)
      continue
    }
    if (existing === part.text) continue // exact replay of one host part
    if (part.text.startsWith(existing) || existing.startsWith(part.text)) {
      // Snapshot growth: keep the longest text seen for the part.
      parts.set(key, existing.length >= part.text.length ? existing : part.text)
      continue
    }
    return refuse(`malformed official stream: text part ${boundedSnippet(part.id)} was replaced with different bytes, not a longer snapshot`)
  }
  if (pinned && lastFinishReason !== "stop") {
    return refuse(lastFinishReason === null
      ? "malformed official stream: the stream carries no terminal step_finish (reason stop) signal"
      : `malformed official stream: the last step_finish reason is "${boundedSnippet(lastFinishReason)}", not the terminal stop`)
  }
  return { texts: order.map((entry) => (typeof entry === "string" ? parts.get(entry) : entry.text)) }
}
