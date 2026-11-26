from __future__ import annotations

import os
import subprocess
from pathlib import Path


def main() -> int:
    source = Path("internal/runner/probe.go")
    candidate = source.read_text()
    previous = subprocess.check_output(["git", "show", "69d9ac18adb29316eb77de2cc81113c74ebde36a:internal/runner/probe.go"], text=True)
    start = "func probeP05(c *probeCtx) *Failure {"
    end = "\nfunc probeP06"
    old_function = previous[previous.index(start):previous.index(end, previous.index(start))]
    first = candidate.index(start)
    last = candidate.index(end, first)
    evidence = Path(os.environ["GITHUB_WORKSPACE"]) / "evidence"
    evidence.mkdir(exist_ok=True)
    log = evidence / "delegation-red.log"
    try:
        source.write_text(candidate[:first] + old_function + candidate[last:])
        with log.open("w") as output:
            code = subprocess.run(["go", "test", "-race", "-count=1", "-v", "-run", "^TestSandboxP05ControllersDelegated/delegated_user_manager$", "./internal/runner"], stdout=output, stderr=subprocess.STDOUT, check=False, timeout=300).returncode
    finally:
        source.write_text(candidate)
    text = log.read_text()
    print(text)
    if code == 0 or "CGROUP_NOT_DELEGATED" not in text or "want pass true" not in text:
        raise RuntimeError(f"old P05 did not fail the intended delegation assertion: exit {code}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
