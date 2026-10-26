#!/usr/bin/env python3
"""Compare explicit expectations with the shared Go producer's retained events.

This reads existing local/CI artifacts. It neither runs tests nor changes their
results, and creates no workflow verdict or replacement timing collector.
"""

import importlib.util
import json
from pathlib import Path
import sys


def read(directory: Path, expectations: list[str]) -> tuple[dict, int]:
    source = Path(__file__).with_name("go-test-results.py")
    spec = importlib.util.spec_from_file_location("go_test_results", source)
    producer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    directory = directory.resolve()
    recorded = json.loads((directory / "results.json").read_text())
    metadata = json.loads((directory / "metadata.json").read_text())
    if recorded.get("schema_version") != 1 or metadata.get("schema_version") != 1:
        raise ValueError("unsupported producer schema")
    code = recorded.get("process_exit_code")
    if code is not None and (type(code) is not int or not 0 <= code <= 255):
        raise ValueError("invalid recorded process exit")
    if metadata.get("process_exit_code") != code:
        raise ValueError("producer process exits disagree")
    expected = {}
    for item in expectations:
        package, separator, name = item.partition("=")
        if not separator or not package or not name or any(c.isspace() for c in item):
            raise ValueError("expectations must be package=ExactTestName")
        expected.setdefault(package, []).append(name)
    selection = {"compile_only": recorded.get("compile_only", False)}
    if expectations:
        selection["expected_tests"] = expected
    if not (directory / "stderr.log").is_file():
        raise ValueError("missing complete stderr diagnostics")
    results = producer.read_go_results(directory / "events.jsonl", code, selection)
    if metadata.get("interrupted_signal") or recorded.get("complete") is not True:
        results["complete"] = False
        results["outcome"] = "fail" if code else "incomplete"
    if expectations and results["outcome"] == "compile_only":
        results["outcome"] = "selection_unproved"
    summary = producer.bounded_summary(results)
    summary["provenance"] = metadata
    summary["artifact_directory"] = str(directory)
    # Preserve a real child failure; proof gaps can turn process success into a
    # failed evidence read, never the other way around.
    return summary, code or (0 if results["outcome"] in {"pass", "compile_only"} else 1)


def main(argv: list[str]) -> int:
    if not argv:
        print("usage: bin/oc-test read-go ARTIFACT_DIR [package=ExactTestName ...]", file=sys.stderr)
        return 64
    try:
        summary, code = read(Path(argv[0]), argv[1:])
    except (OSError, ValueError, TypeError, AttributeError, KeyError) as error:
        print(f"read-go: required producer evidence unavailable: {error}", file=sys.stderr)
        return 1
    print(json.dumps(summary, indent=2))
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
