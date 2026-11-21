#!/usr/bin/env python3
"""Bounded core stand-in used only inside the qualification sandbox."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


def main():
    directory = Path(os.environ["QUALIFICATION_SHIM_DIR"])
    data = sys.stdin.buffer.read()
    argv = sys.argv[1:]
    with (directory / "calls.jsonl").open("a") as log:
        log.write(json.dumps({"cwd": os.getcwd(), "argv": argv, "pid": os.getpid(),
                              "stdin": data.decode("utf-8")}) + "\n")
    real = str(Path(sys.argv[0]).with_name("concord"))
    if argv == ["host-lease"]:
        result = subprocess.run([real, *argv], input=data, capture_output=True)
        if result.returncode:
            sys.stdout.buffer.write(result.stdout)
            sys.stderr.buffer.write(result.stderr)
            return result.returncode
        lines = result.stdout.decode().splitlines()
        lease = json.loads(lines[-1])
        lease["core_binary"] = os.path.abspath(sys.argv[0])
        sys.stdout.write("\n".join(lines[:-1] + [json.dumps(lease)]) + "\n")
        return 0
    mode = (directory / "mode").read_text().strip()
    if mode == "sleep":
        (directory / "sleeping").write_text(str(os.getpid()))
        time.sleep(90)
    if mode == "oversize" and argv == ["project-resolve"]:
        sys.stdout.write(json.dumps({"project_id": "project-qualification", "product_ids": ["qualification"],
                                     "scope_version": "sha256:" + "0" * 64, "main_worktree": False}))
        return 0
    if mode == "oversize" and argv == ["invoke"]:
        result = subprocess.run([real, *argv], input=data, capture_output=True)
        (directory / "real-response").write_bytes(result.stdout)
        if result.returncode:
            sys.stdout.buffer.write(result.stdout)
            sys.stderr.buffer.write(result.stderr)
            return result.returncode
        envelope = json.loads(result.stdout)
        if envelope.get("origin") != "core" or envelope.get("outcome") != "error":
            raise ValueError("real read did not produce the expected core error envelope")
        envelope["error"]["details"] = {"qualification": "x" * 52000}
        oversized = json.dumps(envelope, separators=(",", ":")).encode()
        (directory / "oversize-bytes").write_text(str(len(oversized)))
        sys.stdout.buffer.write(oversized)
        sys.stderr.buffer.write(result.stderr)
        return result.returncode
    with tempfile.TemporaryFile() as input_file:
        input_file.write(data)
        input_file.seek(0)
        os.dup2(input_file.fileno(), 0)
        os.execv(real, [real, *argv])


if __name__ == "__main__":
    raise SystemExit(main())
