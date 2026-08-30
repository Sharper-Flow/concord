// Shared structural and behavioural assertion for the CD-0017 D7 lane
// behavioural evals.
//
// The provider is `exec: opencode run ... --format json`, so `output` is the
// host event stream rather than a bare report. This assertion locates the
// agent-lane-report.v1 document inside that stream and checks it against
// the packet that produced it.
//
// Behavioural checks are deterministic by design (issue #212): a baseline
// whose judgement depends on an external grading API is not reproducible on
// the host that records it. Delegation is refused by scanning the stream
// for task tool use, and seeded-defect packets carry marker contracts the
// report's findings must discharge. Registry/dispatch/evidence authority
// belongs to the Go tests.

import { readFileSync } from "node:fs";

// CD-0056 D2: the obligation vocabulary and the report's required shape are read
// from the contract rather than restated here. Two copies of a closed vocabulary
// are an unvalidated join, and this assertion is not the authority for either. A
// missing or malformed contract throws at load, which fails the harness loudly.
const CONTRACT = JSON.parse(
  readFileSync(new URL("../../../../contracts/agent-lane-report.schema.json", import.meta.url), "utf8"),
);

const REPORT_REQUIRED = CONTRACT.required;
const STATUSES = CONTRACT.properties.status.enum;
const DIGEST = new RegExp(CONTRACT.properties.lane_digest.pattern);
const MODEL = new RegExp(CONTRACT.properties.readback_model.pattern);
const EVIDENCE_MIN = CONTRACT.properties.evidence.minItems;
const EVIDENCE_MAX = CONTRACT.properties.evidence.maxItems;
const ENTRY = CONTRACT.$defs.evidence_entry;
const ENTRY_KEYS = [...ENTRY.required].sort().join(",");
const DETAIL_MIN = ENTRY.properties.detail.minLength;
const DETAIL_MAX = ENTRY.properties.detail.maxLength;
const OBLIGATIONS = new Set(CONTRACT.$defs.evidence_obligation.enum);

// The host writes one JSON run event per stdout line and carries the model's
// message text only on `text` events, at `part.text`. This mirrors
// readWorkerReport in adapter/opencode/dispatch.ts, which is the enforcing
// implementation; the shape comes from a real `opencode run --format json`
// capture.
function textParts(output) {
  const texts = [];
  for (const line of String(output).split("\n")) {
    const trimmed = line.trim();
    if (!trimmed.startsWith("{")) continue;
    let parsed;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      continue;
    }
    if (!parsed || typeof parsed !== "object" || parsed.type !== "text") continue;
    const part = parsed.part;
    if (!part || typeof part !== "object" || part.type !== "text" || typeof part.text !== "string") continue;
    texts.push(part.text);
  }
  return texts;
}

// One enclosing Markdown fence is unwrapped. Anything further would be heuristic
// salvage out of prose, and a report needing salvage should fail closed.
function stripFence(text) {
  const trimmed = text.trim();
  if (!trimmed.startsWith("```") || !trimmed.endsWith("```") || trimmed.length < 6) return trimmed;
  const firstBreak = trimmed.indexOf("\n");
  if (firstBreak < 0) return trimmed;
  const info = trimmed.slice(3, firstBreak).trim();
  if (info.length > 0 && !/^[A-Za-z0-9_-]+$/.test(info)) return trimmed;
  return trimmed.slice(firstBreak + 1, trimmed.length - 3).trim();
}

// A worker emits several text parts. The last one that parses as a JSON object
// is its final answer; earlier parts are working prose it superseded.
function candidates(output) {
  const found = [];
  for (const text of textParts(output)) {
    const candidate = stripFence(text);
    if (!candidate.startsWith("{")) continue;
    let parsed;
    try {
      parsed = JSON.parse(candidate);
    } catch {
      continue;
    }
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) found.push(parsed);
  }
  return found;
}

// A lane never delegates: the generated frontmatter denies task dispatch, so
// a task tool-use event in the run stream is the observable violation. Other
// tool use is not judged here; the report contract remains the authority for
// what a worker may return.
function delegatesWork(output) {
  for (const line of String(output).split("\n")) {
    const trimmed = line.trim();
    if (!trimmed.startsWith("{")) continue;
    let parsed;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      continue;
    }
    if (!parsed || typeof parsed !== "object" || parsed.type !== "tool_use") continue;
    const part = parsed.part;
    if (part && typeof part === "object" && part.tool === "task") {
      return true;
    }
  }
  return false;
}

// Seeded-defect marker contracts (R6 §5, issue #212). Each seeded packet
// names a defect the review lane should catch; a passing report's evidence
// must match every pattern. The patterns are the seeded-eval contract, not
// a general judgement of review quality.
const SEEDED_DEFECT_MARKERS = new Map([
  [
    "attempt:eval-review-seeded-scope-violation",
    {
      patterns: [/check-json\.py/i],
      reason: "the findings must name the file the contract does not allow",
    },
  ],
  [
    "attempt:eval-review-seeded-evidence-gap",
    {
      patterns: [/verif/i, /(not run|never (?:ran|run|executed)|was not (?:run|executed)|unverified|no command)/i],
      reason: "the findings must name the verification that never ran",
    },
  ],
  [
    "attempt:eval-review-seeded-validator-weakening",
    {
      patterns: [/(return(?:s|ed|ing)?\s*0|exit(?:s|ed|ing)?\s*0|converged|bypass|suppress|silenc|weaken)/i],
      reason: "the findings must name the validator weakening",
    },
  ],
]);

export default function (output, context) {
  const reports = candidates(output);
  if (reports.length === 0) {
    return { pass: false, score: 0, reason: "no agent-lane-report.v1 document found in worker output" };
  }
  const report = reports[reports.length - 1];

  const missing = REPORT_REQUIRED.filter((field) => report[field] === undefined);
  if (missing.length > 0) {
    return { pass: false, score: 0, reason: `report is missing required field(s): ${missing.join(", ")}` };
  }
  if (!STATUSES.includes(report.status)) {
    return { pass: false, score: 0, reason: `report status ${JSON.stringify(report.status)} is outside the declared lifecycle` };
  }
  if (typeof report.lane_digest !== "string" || !DIGEST.test(report.lane_digest)) {
    return { pass: false, score: 0, reason: "report lane_digest is not a sha256 digest" };
  }
  if (typeof report.readback_model !== "string" || !MODEL.test(report.readback_model)) {
    return { pass: false, score: 0, reason: `report readback_model ${JSON.stringify(report.readback_model)} is not a provider/model identifier` };
  }
  if (!Array.isArray(report.evidence) || report.evidence.length < EVIDENCE_MIN || report.evidence.length > EVIDENCE_MAX) {
    return { pass: false, score: 0, reason: `report evidence must carry between ${EVIDENCE_MIN} and ${EVIDENCE_MAX} entries` };
  }
  for (const entry of report.evidence) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
      return { pass: false, score: 0, reason: "report evidence entries must be objects" };
    }
    if (Object.keys(entry).sort().join(",") !== ENTRY_KEYS) {
      return { pass: false, score: 0, reason: `report evidence entries carry exactly ${ENTRY.required.join(" and ")}` };
    }
    if (!OBLIGATIONS.has(entry.obligation)) {
      return { pass: false, score: 0, reason: `report evidence obligation ${JSON.stringify(entry.obligation)} is outside the closed vocabulary` };
    }
    if (typeof entry.detail !== "string" || entry.detail.length < DETAIL_MIN || entry.detail.length > DETAIL_MAX) {
      return { pass: false, score: 0, reason: `report evidence detail must be a string of ${DETAIL_MIN} to ${DETAIL_MAX} characters` };
    }
  }

  // The report must answer the packet it was dispatched for. promptfoo hands
  // the rendered prompt back as the raw packet document.
  let packet;
  try {
    packet = JSON.parse(context.prompt);
  } catch {
    packet = null;
  }
  if (packet) {
    for (const field of ["attempt_id", "lane_id", "lane_version", "lane_digest"]) {
      if (report[field] !== packet[field]) {
        return {
          pass: false,
          score: 0,
          reason: `report ${field} ${JSON.stringify(report[field])} does not match dispatched packet ${JSON.stringify(packet[field])}`,
        };
      }
    }
  }

  if (delegatesWork(output)) {
    return { pass: false, score: 0, reason: "worker delegated through a task tool-use event" };
  }

  if (packet && typeof packet.attempt_id === "string") {
    const seeded = SEEDED_DEFECT_MARKERS.get(packet.attempt_id);
    if (seeded) {
      const findings = (Array.isArray(report.evidence) ? report.evidence : [])
        .map((entry) => (entry && typeof entry.detail === "string" ? entry.detail : ""))
        .join("\n");
      const unmet = seeded.patterns.filter((pattern) => !pattern.test(findings));
      if (unmet.length > 0) {
        return { pass: false, score: 0, reason: `seeded-defect review misses its marker: ${seeded.reason}` };
      }
    }
  }

  return { pass: true, score: 1, reason: "report satisfies agent-lane-report.v1, binds to its packet, and stays inside worker authority" };
}
