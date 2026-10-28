#!/usr/bin/env python3
"""Small guard mutations; never count build failures/timeouts as kills."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
mutants = [
    ("request attempt ceiling", "job.go", "r.MaxAttempts > MaxAttempts",
     "false", "TestRequestValidation", False),
    ("stored metadata validation", "worker.go", "if job.Validate() != nil {",
     "if false {", "TestWorkerOutcomes/invalid", False),
    ("retry cap", "worker.go", "return w.config.RetryMax",
     "return delay", "TestConfigBackoffAndSanitizedErrors", False),
    ("claim owner fence", "stores/postgres/queries.sql", "claim_token = $2",
     "(claim_token = $2 OR true)", "TestLeaseExpiryFencesStaleOwners", True),
    ("expired acknowledgement fence", "stores/postgres/queries.sql",
     "AND lease_until > clock_timestamp()",
     "AND (lease_until > clock_timestamp() OR true)",
     "TestLeaseExpiryFencesStaleOwners", True),
    ("abandoned attempt ceiling", "stores/postgres/queries.sql",
     "j.attempts >= j.max_attempts", "j.attempts > j.max_attempts",
     "TestDelayRetryAndCrashExhaustion", True),
]
counts = dict(killed=0, survived=0, build_failure=0, timeout=0, skipped=0)


def run(command, cwd):
    return subprocess.run(command, cwd=cwd, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=60, check=False)


# Establish that the selected baseline tests pass, so ordinary failures cannot
# masquerade as mutation kills.
for module in [".", "./stores/postgres"]:
    if module != "." and not os.getenv("PLUTO_JOBS_TEST_DATABASE_URL"):
        continue
    baseline = run(["go", "test", "-count=1", module], root)
    if baseline.returncode:
        raise SystemExit("Baseline failed:\n" + baseline.stdout)

for name, file, old, new, test, sql in mutants:
    if sql and not os.getenv("PLUTO_JOBS_TEST_DATABASE_URL"):
        counts["skipped"] += 1
        print(f"SKIPPED: {name} (database URL absent)")
        continue
    with tempfile.TemporaryDirectory(prefix=".mutation-", dir=root) as tmp:
        target = Path(tmp)
        for entry in root.iterdir():
            if entry.name.startswith((".git", ".mutation-")) or entry.name in {
                    "go.work", "go.work.sum"}:
                continue
            if entry.is_dir():
                shutil.copytree(entry, target / entry.name)
            else:
                shutil.copy2(entry, target / entry.name)
        (target / "go.work").write_text(
            "go 1.25.5\nuse (\n.\n./stores/postgres\n)\n"
            "replace github.com/plutoframework/pluto-jobs v0.1.0 => .\n")
        path = target / file
        source = path.read_text()
        if old not in source:
            raise SystemExit(f"Missing mutation site: {name}")
        # The abandoned-attempt mutation must alter every matching CASE arm.
        path.write_text(source.replace(old, new, -1 if "abandoned" in name else 1))
        try:
            if sql:
                generated = run([
                    "env", "GOWORK=off", "go", "run",
                    "github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0", "generate",
                ], target / "stores/postgres")
                if generated.returncode:
                    counts["build_failure"] += 1
                    print(f"BUILD FAILURE: {name}\n{generated.stdout}")
                    continue
            package = "./stores/postgres" if sql else "."
            result = run(["go", "test", "-count=1", "-run", test, package], target)
            if result.returncode == 0:
                status = "survived"
            elif "[build failed]" in result.stdout or "[setup failed]" in result.stdout:
                status = "build_failure"
            elif "--- FAIL:" in result.stdout or "panic:" in result.stdout:
                status = "killed"
            else:
                status = "build_failure"
            counts[status] += 1
            print(f"{status.upper()}: {name}")
            if status != "killed":
                print(result.stdout)
        except subprocess.TimeoutExpired:
            counts["timeout"] += 1
            print(f"TIMEOUT: {name}")
print(counts)
if counts["survived"] or counts["build_failure"] or counts["timeout"]:
    raise SystemExit(1)
