#!/usr/bin/env python3
"""Validate and deterministically generate branch-freshness result projections.

The owning declaration `contracts/branch-freshness.v1.json` is the single
source of the work_start and work_resume `branch_freshness` result contract:
the closed statuses, the typed failure reasons, the sampled SHA and origin
default-ref contracts, and the exact field set each status carries. This
script validates the declaration against its schema, derives positive and
negative contract fixtures, and projects the closed vocabulary plus a strict
JSON-shape validator into Go. The Go and adapter tests apply the same
fixtures to their own validators, so a projection that drifts from the
declaration fails its owning suite.
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

from vocabulary_utils import check_schema_keywords, digest, fail, schema_validate


ROOT = Path(__file__).resolve().parents[1]
DECLARATION = ROOT / "contracts/branch-freshness.v1.json"
SCHEMA = ROOT / "contracts/branch-freshness.schema.json"
FIXTURES = ROOT / "contracts/branch-freshness.fixtures.json"
DIGEST = ROOT / "contracts/branch-freshness.digest"
GO_PROJECTION = ROOT / "internal/store/generated_branch_freshness.go"


def load(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        fail(f"{path.relative_to(ROOT)}: {exc}")


def status_by_name(declaration: dict, name: str) -> dict:
    for status in declaration["statuses"]:
        if status["status"] == name:
            return status
    fail(f"declaration is missing the {name} status")


def check_declaration(declaration: dict) -> None:
    names = [status["status"] for status in declaration["statuses"]]
    if names != ["ok", "unknown"]:
        fail(f"declaration statuses must be exactly [ok, unknown], got {names}")
    ok = status_by_name(declaration, "ok")
    unknown = status_by_name(declaration, "unknown")
    if ok["fields"][0] != "status" or unknown["fields"][0] != "status":
        fail("every status field set must start with the status field")
    for field in ok.get("sha_fields", []):
        if field not in ok["fields"]:
            fail(f"ok sha field {field} is not an ok field")
    if ok.get("count_field") not in ok["fields"]:
        fail("ok count_field is not an ok field")
    if unknown.get("reason_field") not in unknown["fields"]:
        fail("unknown reason_field is not an unknown field")
    if not declaration["failure_reasons"]:
        fail("declaration carries no failure reasons")
    try:
        re.compile(declaration["sha_pattern"])
    except re.error as exc:
        fail(f"declaration sha_pattern does not compile: {exc}")
    prefix = declaration["default_ref"]["prefix"]
    if not prefix.endswith("/"):
        fail("declaration default_ref prefix must end with /")


def build_fixtures(declaration: dict) -> dict:
    ok = status_by_name(declaration, "ok")
    unknown = status_by_name(declaration, "unknown")
    prefix = declaration["default_ref"]["prefix"]
    head, default = "a" * 40, "c" * 40
    ok_sample = {
        "status": "ok",
        ok["sha_fields"][0]: head,
        "default_ref": prefix + "main",
        ok["sha_fields"][1]: default,
        ok["count_field"]: 0,
    }
    positive = [
        ok_sample,
        {**ok_sample, "default_ref": prefix + "release/stable", ok["count_field"]: 2},
        {**ok_sample, ok["count_field"]: 41},
    ]
    for reason in declaration["failure_reasons"]:
        positive.append({"status": "unknown", unknown["reason_field"]: reason})
    negative = [
        # wrong status vocabulary
        {**ok_sample, "status": "degraded"},
        # ok variant: missing field, extra field, wrong sha shapes, bad refs,
        # and a count that is negative or fractional
        {key: value for key, value in ok_sample.items() if key != ok["count_field"]},
        {**ok_sample, "reason": "timeout"},
        {**ok_sample, ok["sha_fields"][0]: "short"},
        {**ok_sample, ok["sha_fields"][0]: head.upper()},
        {**ok_sample, ok["sha_fields"][0]: ["a" * 40]},
        {**ok_sample, ok["sha_fields"][1]: "short"},
        {**ok_sample, "default_ref": "main"},
        {**ok_sample, "default_ref": prefix},
        {**ok_sample, "default_ref": prefix + "release//stable"},
        {**ok_sample, "default_ref": prefix + "release/stable/"},
        {**ok_sample, "default_ref": prefix + "release/stable."},
        {**ok_sample, "default_ref": prefix + "release/.stable"},
        {**ok_sample, "default_ref": prefix + "release/stable.lock"},
        {**ok_sample, "default_ref": prefix + "release..stable"},
        {**ok_sample, "default_ref": prefix + "release@{stable"},
        {**ok_sample, "default_ref": prefix + "release stable"},
        {**ok_sample, ok["count_field"]: -1},
        {**ok_sample, ok["count_field"]: 1.5},
        {**ok_sample, ok["count_field"]: "2"},
        # unknown variant: extra field, invented reason, wrong reason type
        {"status": "unknown", unknown["reason_field"]: "timeout", ok["count_field"]: 4},
        {"status": "unknown", unknown["reason_field"]: "invented_reason"},
        {"status": "unknown", unknown["reason_field"]: 7},
    ]
    return {"positive": positive, "negative": negative}


def go_projection(declaration: dict) -> str:
    ok = status_by_name(declaration, "ok")
    unknown = status_by_name(declaration, "unknown")
    sha_fields = ", ".join(json.dumps(field) for field in ok["sha_fields"])
    fields_literal = "\n".join(
        f"\t{json.dumps(status['status'])}: {{{', '.join(json.dumps(field) for field in status['fields'])}}},"
        for status in declaration["statuses"]
    )
    reasons_literal = "\n".join(
        f"\t{json.dumps(reason)}: true," for reason in declaration["failure_reasons"]
    )
    statuses_list = ", ".join(json.dumps(status["status"]) for status in declaration["statuses"])
    reasons_list = ", ".join(json.dumps(reason) for reason in declaration["failure_reasons"])
    prefix = declaration["default_ref"]["prefix"]
    count_field = ok["count_field"]
    reason_field = unknown["reason_field"]
    minimum = declaration["behind_count_minimum"]
    return f'''// Code generated by scripts/generate-branch-freshness.py; DO NOT EDIT.

package store

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// branchFreshnessSHAPattern matches the full commit SHA the owning
// branch-freshness contract requires for every sampled SHA field.
var branchFreshnessSHAPattern = regexp.MustCompile({json.dumps(declaration["sha_pattern"])})

// branchFreshnessFields is the closed field set each status carries, in
// contract order.
var branchFreshnessFields = map[string][]string{{
{fields_literal}
}}

// BranchFreshnessStatusAllowed reports whether status is in the closed
// branch-freshness status vocabulary.
func BranchFreshnessStatusAllowed(status string) bool {{
	_, ok := branchFreshnessFields[status]
	return ok
}}

// branchFreshnessReasons is the closed typed failure-reason vocabulary.
var branchFreshnessReasons = map[string]bool{{
{reasons_literal}
}}

// BranchFreshnessReasonAllowed reports whether reason is in the closed
// branch-freshness typed failure-reason vocabulary.
func BranchFreshnessReasonAllowed(reason string) bool {{
	return branchFreshnessReasons[reason]
}}

// branchFreshnessDefaultRefValid reports whether ref is the {json.dumps(prefix)} prefix
// plus a branch name git-check-ref-format accepts, per the owning contract's
// git_ref_rule: no control character, space, ~ ^ : ? * [ or backslash
// anywhere, no .., no @{{, no leading or trailing slash or double slash, no
// trailing dot, and no slash-separated component that starts with a dot or
// ends with .lock.
func branchFreshnessDefaultRefValid(ref string) bool {{
	const prefix = {json.dumps(prefix)}
	if !strings.HasPrefix(ref, prefix) || len(ref) == len(prefix) {{
		return false
	}}
	name := "refs/remotes/" + ref
	for _, r := range name {{
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("~^:?*[\\\\", r) {{
			return false
		}}
	}}
	if strings.Contains(name, "..") || strings.Contains(name, "@{{") {{
		return false
	}}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {{
		return false
	}}
	if strings.HasSuffix(name, ".") {{
		return false
	}}
	for _, component := range strings.Split(name, "/") {{
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {{
			return false
		}}
	}}
	return true
}}

// ValidateBranchFreshnessObject validates one decoded branch_freshness result
// object against the owning contract: a closed status, the exact field set
// that status carries, {json.dumps(declaration["sha_pattern"])} sampled SHAs and a
// valid origin default ref and a nonnegative integer {json.dumps(count_field)} behind the
// ok status, and a closed typed failure reason behind the unknown status.
func ValidateBranchFreshnessObject(value map[string]any) error {{
	status, ok := value["status"].(string)
	if !ok {{
		return fmt.Errorf("branch_freshness status is not a string")
	}}
	fields, known := branchFreshnessFields[status]
	if !known {{
		return fmt.Errorf("branch_freshness status %q is not in the owning contract", status)
	}}
	if len(value) != len(fields) {{
		return fmt.Errorf("branch_freshness %s carries %d fields, want exactly %d", status, len(value), len(fields))
	}}
	for _, field := range fields {{
		if _, present := value[field]; !present {{
			return fmt.Errorf("branch_freshness %s is missing field %q", status, field)
		}}
	}}
	if status != "ok" {{
		reason, _ := value[{json.dumps(reason_field)}].(string)
		if !branchFreshnessReasons[reason] {{
			return fmt.Errorf("branch_freshness reason %q is not in the owning contract", reason)
		}}
		return nil
	}}
	for _, field := range []string{{{sha_fields}}} {{
		sha, _ := value[field].(string)
		if !branchFreshnessSHAPattern.MatchString(sha) {{
			return fmt.Errorf("branch_freshness %s %q is not a full commit SHA", field, sha)
		}}
	}}
	ref, _ := value["default_ref"].(string)
	if !branchFreshnessDefaultRefValid(ref) {{
		return fmt.Errorf("branch_freshness default_ref %q is not a valid origin default ref", ref)
	}}
	count, isNumber := value[{json.dumps(count_field)}].(float64)
	if !isNumber || count != math.Trunc(count) || count < {minimum} {{
		return fmt.Errorf("branch_freshness {count_field} is not an integer of at least {minimum}")
	}}
	return nil
}}

// BranchFreshnessStatuses returns the closed status vocabulary in contract order.
func BranchFreshnessStatuses() []string {{
	return []string{{{statuses_list}}}
}}

// BranchFreshnessReasons returns the closed typed failure reasons in contract order.
func BranchFreshnessReasons() []string {{
	return []string{{{reasons_list}}}
}}
'''


def main() -> int:
    try:
        declaration = load(DECLARATION)
        schema = load(SCHEMA)
        check_schema_keywords(schema)
        schema_validate(declaration, schema, schema, "declaration")
        check_declaration(declaration)
        fixtures = build_fixtures(declaration)
        vocabulary_digest = digest(declaration)
        go_source = subprocess.run(
            ["gofmt"], input=go_projection(declaration), text=True, capture_output=True, check=True
        ).stdout
        expected = {
            FIXTURES: json.dumps(fixtures, ensure_ascii=False, indent=2) + "\n",
            GO_PROJECTION: go_source,
            DIGEST: vocabulary_digest + "\n",
        }
        if "--check" in sys.argv[1:]:
            for path, content in expected.items():
                if not path.is_file() or path.read_text(encoding="utf-8") != content:
                    fail(f"generated branch-freshness drift: {path.relative_to(ROOT)}")
        else:
            for path, content in expected.items():
                path.parent.mkdir(exist_ok=True)
                path.write_text(content, encoding="utf-8")
        print(vocabulary_digest)
        return 0
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"branch-freshness generation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
