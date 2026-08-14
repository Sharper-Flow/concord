// Shared structural assertion for the CD-0017 D7 lane behavioural evals.
//
// The provider is `exec: opencode run ... --format json`, so `output` is the
// host event stream rather than a bare report. This assertion locates the
// agent-lane-report.v1 document inside that stream and checks it against the
// packet that produced it.
//
// This is a structural check only. It deliberately asserts nothing about
// wording; behavioural judgement belongs to the llm-rubric assertion, and
// registry/dispatch/evidence authority belongs to the Go tests.

const REPORT_REQUIRED = [
  "schema_version",
  "attempt_id",
  "lane_id",
  "lane_version",
  "lane_digest",
  "readback_model",
  "status",
  "evidence",
];

const DIGEST = /^sha256:[0-9a-f]{64}$/;
const MODEL = /^[a-z][a-z0-9_.-]*\/[^/ ]+$/;

function candidates(output) {
  const found = [];
  for (const line of String(output).split("\n")) {
    const trimmed = line.trim();
    if (!trimmed.startsWith("{")) continue;
    let parsed;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      continue;
    }
    const nested = [parsed];
    for (const key of ["report", "result", "output", "data"]) {
      if (parsed && typeof parsed[key] === "object" && parsed[key] !== null) nested.push(parsed[key]);
      if (typeof parsed?.[key] === "string") {
        try {
          nested.push(JSON.parse(parsed[key]));
        } catch {
          // A non-JSON string on a known key is ordinary host chatter.
        }
      }
    }
    for (const value of nested) {
      if (value && typeof value === "object" && value.schema_version === "1.0" && value.lane_id) {
        found.push(value);
      }
    }
  }
  return found;
}

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
  if (!["completed", "failed"].includes(report.status)) {
    return { pass: false, score: 0, reason: `report status ${JSON.stringify(report.status)} is outside the declared lifecycle` };
  }
  if (!DIGEST.test(report.lane_digest)) {
    return { pass: false, score: 0, reason: "report lane_digest is not a sha256 digest" };
  }
  if (!MODEL.test(report.readback_model)) {
    return { pass: false, score: 0, reason: `report readback_model ${JSON.stringify(report.readback_model)} is not a provider/model identifier` };
  }
  if (!Array.isArray(report.evidence) || report.evidence.length < 1 || report.evidence.length > 64) {
    return { pass: false, score: 0, reason: "report evidence must carry between 1 and 64 entries" };
  }
  if (report.evidence.some((entry) => typeof entry !== "string" || entry.length < 1 || entry.length > 512)) {
    return { pass: false, score: 0, reason: "report evidence entries must be strings of 1 to 512 characters" };
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

  return { pass: true, score: 1, reason: "report satisfies agent-lane-report.v1 and binds to its packet" };
}
