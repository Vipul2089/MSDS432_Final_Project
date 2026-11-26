#!/usr/bin/env python3
"""Failure tests FT-01 and FT-02 from the low-level design (LLD-001 §10.2).

Run from the repository root, with the stack up and at least one finished day
of data loaded:

    make up
    make load args="-target-gb 0.2 -backfill 72h"
    make test-failure-dlm-crash          # FT-01
    make test-failure-lost-cold-file     # FT-02

The test holds sealed partitions in WARM, starts a WARM_TO_COLD job for the
largest one, injects the fault and searches for one known event of that
partition every second until the job is DONE. The active policy is restored
afterwards. Exit status 0 means the test passed.

dlm-crash       FT-01: dlm is killed with SIGKILL while the job is COPYING.
lost-cold-file  FT-02: dlm runs with DLM_TEST_HOOK=delete_part_before_verify,
                so the first attempt loses a Parquet file before it is verified.
"""
import json
import os
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

COMPOSE = ["docker", "compose", "--env-file", ".env", "-f", "deploy/compose/compose.yaml"]
HOOK = "delete_part_before_verify"
TESTS = {"dlm-crash": "FT-01", "lost-cold-file": "FT-02"}


def log(msg):
    print(time.strftime("%H:%M:%S"), msg, flush=True)


def env_file(key, default=None):
    for line in open(".env"):
        k, _, v = line.strip().partition("=")
        if k == key and v:
            return v
    if default is None:
        sys.exit(f".env has no {key}")
    return default


def compose(*args, env=None):
    return subprocess.run(COMPOSE + list(args), check=True, capture_output=True, text=True, env=env).stdout.strip()


def sql(query):
    return compose("exec", "-T", "postgres", "psql", "-U", env_file("POSTGRES_USER"), "-d", env_file("POSTGRES_DB"),
                   "-At", "-F", "|", "-c", query)


def wait_for(what, fn, timeout):
    end = time.time() + timeout
    while time.time() < end:
        value = fn()
        if value:
            return value
        time.sleep(1)
    raise RuntimeError(f"timed out after {timeout}s waiting for {what}")


def search(**params):
    url = f"http://localhost:{env_file('ROUTER_PORT', '8080')}/api/v1/search?"
    try:
        with urllib.request.urlopen(url + urllib.parse.urlencode(params), timeout=30) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, {}
    except OSError as e:
        return 0, {"error": str(e)}


class SearchLoop(threading.Thread):
    """Looks up one event every second (requirement FR-04) and records the partition's
    catalog location, so FT-02 can show it never pointed at a broken copy."""

    def __init__(self, event_id, start, end, partition_id):
        super().__init__(daemon=True)
        self.params = {"event_id": event_id, "from": start, "to": end}
        self.partition_id = partition_id
        self.stop = threading.Event()
        self.searches, self.failures, self.tiers, self.locations = 0, [], [], []

    def run(self):
        while not self.stop.is_set():
            t = time.time()
            code, body = search(**self.params)
            hits = [h for h in body.get("hits", []) if h["event_id"] == self.params["event_id"]]
            self.searches += 1
            if code != 200 or body.get("status") != "COMPLETE" or len(hits) != 1:
                self.failures.append((time.strftime("%H:%M:%S"), code, body.get("status"), len(hits)))
            elif not self.tiers or self.tiers[-1] != hits[0]["tier"]:
                self.tiers.append(hits[0]["tier"])
            loc = sql(f"SELECT location FROM data_partitions WHERE partition_id = {self.partition_id}")
            if not self.locations or self.locations[-1] != loc:
                self.locations.append(loc)
            self.stop.wait(max(0.0, 1 - (time.time() - t)))


def crash_dlm():
    """Sends SIGKILL to the dlm process. tini (PID 1) then exits, and Docker
    restarts the container under its restart policy."""
    container = compose("ps", "-q", "dlm")
    before = int(subprocess.check_output(["docker", "inspect", "-f", "{{.RestartCount}}", container], text=True))
    pid = compose("exec", "-T", "dlm", "sh", "-c",
                  'for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = dlm ] && basename $p; done')
    compose("exec", "-T", "dlm", "sh", "-c", f"kill -9 {int(pid)}")
    log(f"sent SIGKILL to dlm (pid {pid})")
    wait_for("Docker to restart dlm", lambda: int(subprocess.check_output(
        ["docker", "inspect", "-f", "{{.RestartCount}}", container], text=True)) > before, 60)
    log("Docker restarted dlm")


def restart_dlm(hook):
    env = dict(os.environ, DLM_TEST_HOOK=hook)
    compose("up", "-d", "--wait", "dlm", env=env)


def run(test):
    checks = []
    # Seal finished days quickly, move them to WARM, and keep them there so
    # that only the job created below copies data to COLD.
    sql("UPDATE tier_policies SET late_window = '1 minute', hot_after = '0', "
        "warm_after = retain_for - interval '1 minute', purge_grace = '2 minutes' WHERE is_active")
    if test == "lost-cold-file":
        restart_dlm(HOOK)
        log(f"dlm restarted with DLM_TEST_HOOK={HOOK}")

    row = wait_for("a WARM partition", lambda: sql(
        "SELECT partition_id, bucket_start AT TIME ZONE 'UTC', bucket_end AT TIME ZONE 'UTC', doc_count "
        "FROM data_partitions p WHERE tier = 'WARM' AND state = 'SEALED' AND NOT EXISTS "
        "(SELECT 1 FROM migration_jobs j WHERE j.partition_id = p.partition_id AND j.status NOT IN ('DONE', 'FAILED')) "
        "ORDER BY doc_count DESC LIMIT 1"), 600)
    pid, start, end, docs = row.split("|")
    start, end = start.replace(" ", "T") + "Z", end.replace(" ", "T") + "Z"
    _, body = search(**{"from": start, "to": end, "limit": 1})
    event_id = body["hits"][0]["event_id"]
    log(f"partition {pid} ({start[:10]}, {docs} events); searching for event {event_id} every second")

    loop = SearchLoop(event_id, start, end, pid)
    loop.start()
    time.sleep(3)
    job = sql(f"INSERT INTO migration_jobs (partition_id, job_type) VALUES ({pid}, 'WARM_TO_COLD') RETURNING job_id")
    job = job.splitlines()[0]
    log(f"started WARM_TO_COLD job {job}")
    status = lambda: sql(f"SELECT status FROM migration_jobs WHERE job_id = {job}")

    if test == "dlm-crash":
        wait_for("COPYING", lambda: status() == "COPYING", 120)
        time.sleep(4)
        crash_dlm()

    seen = []

    def finished():
        s = status()
        if not seen or seen[-1] != s:
            seen.append(s)
            log(f"job {job}: {s}")
        return s in ("DONE", "FAILED")

    wait_for("the job to finish", finished, 900)
    time.sleep(3)
    loop.stop.set()
    loop.join()

    st, attempt, src, dst, fp_ok, error = sql(
        f"SELECT status, attempt, src_count, dst_count, src_fingerprint = dst_fingerprint, coalesce(error_msg, '') "
        f"FROM migration_jobs WHERE job_id = {job}").split("|", 5)
    location = sql(f"SELECT location FROM data_partitions WHERE partition_id = {pid}")
    checks += [
        ("job reached DONE", st == "DONE"),
        (f"dst_count = doc_count ({dst} = {docs})", dst == docs == src),
        ("fingerprints match", fp_ok == "t"),
        (f"no search failed ({loop.searches} searches)", not loop.failures),
        (f"event served from WARM, then COLD ({' -> '.join(loop.tiers)})", loop.tiers == ["WARM", "COLD"]),
    ]
    if test == "lost-cold-file":
        checks += [
            # ROLLING_BACK lasts a moment, so the saved error and attempt are the evidence.
            (f"verification caught the missing file, job retried ({error[:40]}...)",
             error.startswith("check part-") and attempt == "2"),
            ("catalog never pointed at attempt 1", not any("attempt=1/" in loc for loc in loop.locations)),
            ("catalog points at attempt 2", "attempt=2/" in location),
        ]
    if loop.failures:
        log(f"failed searches: {loop.failures[:5]}")
    return checks


def main():
    test = sys.argv[1] if len(sys.argv) == 2 else ""
    if test not in TESTS:
        sys.exit("usage: run.py " + "|".join(TESTS))
    policy = sql("SELECT late_window, hot_after, warm_after, purge_grace FROM tier_policies WHERE is_active").split("|")
    try:
        checks = run(test)
    finally:
        sql("UPDATE tier_policies SET late_window = '{}', hot_after = '{}', warm_after = '{}', purge_grace = '{}' "
            "WHERE is_active".format(*policy))
        if test == "lost-cold-file":
            restart_dlm("")
        log("policy restored")

    print()
    for name, ok in checks:
        print(("PASS  " if ok else "FAIL  ") + name)
    passed = all(ok for _, ok in checks)
    print(f"\nFailure test {test} {'PASSED' if passed else 'FAILED'}")
    sys.exit(0 if passed else 1)


if __name__ == "__main__":
    main()
