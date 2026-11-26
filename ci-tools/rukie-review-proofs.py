from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


SOURCE = Path(os.environ["GITHUB_WORKSPACE"]) / "candidate"
EVIDENCE = Path(os.environ["GITHUB_WORKSPACE"]) / "evidence"
HEAD = "3911669b0f399472d5c436cff0819dbafae99c67"
TREE = "f7482e298874a77920a9542e2fbb2c7881c851c5"


@dataclass(frozen=True)
class Mutation:
    label: str
    path: str
    original: str
    replacement: str
    test: str
    failure: str


MUTATIONS = (
    Mutation("lint-recognition", "tools/passgate.sh", r"|^make.*\*\*\*", "", "^TestPassgateReportsEveryNestedFailureOnce$", "aggregate annotation omits the independent failure"),
    Mutation("vet-recognition", "tools/passgate.sh", r"|\.go:[0-9]+:", "", "^TestPassgateReportsEveryNestedFailureOnce$", "aggregate annotation omits the file-scoped failure"),
    Mutation("nested-path", "internal/runner/passgate_test.go", 'bash "$1" NEVER', 'bash $1 NEVER', "^TestPassgateReportsEveryNestedFailureOnce$", "aggregate annotation omits the nested gate failure"),
    Mutation("utf8-sequence", "internal/runner/probe.go", 'strings.ToValidUTF8(string(stderr), "\\uFFFD")', 'string([]rune(string(stderr)))', "^TestSandboxP10ProbeImage$/^truncated_sequence$", "want E_PROBE_IMAGE/75"),
)


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=SOURCE, text=True).strip()


def identity() -> None:
    assert git("rev-parse", "HEAD") == HEAD
    assert git("write-tree") == TREE
    files: dict[str, str] = {}
    for relative in git("diff", "--cached", "--name-only").splitlines():
        source = SOURCE / relative
        target = EVIDENCE / "source" / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        files[relative] = hashlib.sha256(source.read_bytes()).hexdigest()
    (EVIDENCE / "identity.json").write_text(json.dumps({"head": HEAD, "tree": TREE, "files": files}, indent=2) + "\n")


def run(command: list[str], log: Path) -> int:
    print(f"Running {command!r}; log {log.name}", flush=True)
    with log.open("w") as output:
        try:
            code = subprocess.run(command, cwd=SOURCE, stdout=output, stderr=subprocess.STDOUT, timeout=180, check=False).returncode
        except subprocess.TimeoutExpired:
            output.write("\nCommand exceeded 180 seconds.\n")
            code = 124
    print(f"Exit {code}: {log.name}", flush=True)
    if code:
        print(log.read_text(errors="replace")[-5000:], flush=True)
    return code


def proofs() -> int:
    results: dict[str, dict[str, object]] = {}
    for mutation in MUTATIONS:
        path = SOURCE / mutation.path
        original = path.read_text()
        assert original.count(mutation.original) == 1, mutation.label
        command = ["go", "test", "-race", "-count=1", "-v", "-run", mutation.test, "./internal/runner"]
        red_log = EVIDENCE / f"{mutation.label}-red.log"
        try:
            path.write_text(original.replace(mutation.original, mutation.replacement, 1))
            red = run(command, red_log)
            reproduced = red != 0 and mutation.failure in red_log.read_text(errors="replace")
        finally:
            path.write_text(original)
        green = run(command, EVIDENCE / f"{mutation.label}-green.log")
        results[mutation.label] = {"command": command, "red": red, "reproduced": reproduced, "green": green}
        (EVIDENCE / "proof-results.json").write_text(json.dumps(results, indent=2) + "\n")
    git("diff", "--exit-code")
    assert git("write-tree") == TREE
    return int(not all(result["reproduced"] is True and result["green"] == 0 for result in results.values()))


def main() -> int:
    EVIDENCE.mkdir(exist_ok=True)
    if sys.argv[1:] == ["identity"]:
        identity()
        return 0
    assert sys.argv[1:] == ["proofs"]
    return proofs()


if __name__ == "__main__":
    raise SystemExit(main())
