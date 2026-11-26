#!/usr/bin/env bash
# Runs the lifecycle demo for requirement FR-03 (SRS-001): switches to the demo policy, loads a 6-hour
# backfill and waits until data sits in all three tiers. Needs a running stack
# with an empty catalog, so start from scratch with: make clean up demo
set -euo pipefail

COMPOSE=(docker compose --env-file .env -f deploy/compose/compose.yaml)
TIMEOUT_S=900
set -a; . ./.env; set +a
ROUTER="localhost:${ROUTER_PORT:-8080}"
GRAFANA="http://localhost:${GRAFANA_PORT:-3000}"

sql() { "${COMPOSE[@]}" exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At -F ' ' -c "$1"; }
say() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

[[ -f data/raw/nslkdd/KDDTrain+.txt ]] || { echo "NSL-KDD data is missing: run make eda-data first" >&2; exit 1; }
for _ in $(seq 60); do curl -sf "$ROUTER/readyz" >/dev/null && break; sleep 2; done
curl -sf "$ROUTER/readyz" >/dev/null || { echo "The stack is not ready: run make up first" >&2; exit 1; }
if [[ "$(sql 'SELECT count(*) FROM data_partitions')" != 0 ]]; then
  echo "The demo needs an empty catalog, because it switches to hourly partitions." >&2
  echo "Start from scratch with: make clean up demo" >&2
  exit 1
fi

say "Activating the demo policy (hourly buckets; WARM after 1 h, COLD after 3 h)"
sql "BEGIN; UPDATE tier_policies SET is_active = false; UPDATE tier_policies SET is_active = true WHERE policy_name = 'demo'; COMMIT;" >/dev/null

say "Loading a 6-hour backfill"
"${COMPOSE[@]}" run --rm loadgen -target-gb 0.3 -backfill 6h

start=$(date +%s)
say "Waiting for data in HOT, WARM and COLD (dlm checks every 30 s)"
while :; do
  read -r hot warm cold jobs < <(sql "SELECT count(*) FILTER (WHERE tier = 'HOT'), count(*) FILTER (WHERE tier = 'WARM'),
      count(*) FILTER (WHERE tier = 'COLD'),
      (SELECT count(*) FROM migration_jobs WHERE status NOT IN ('DONE', 'FAILED'))
    FROM data_partitions WHERE state <> 'DELETED'")
  say "partitions: HOT $hot, WARM $warm, COLD $cold; jobs running or queued: $jobs"
  (( hot > 0 && warm > 0 && cold > 0 )) && break
  (( $(date +%s) - start > TIMEOUT_S )) && { say "Not all tiers have data after $((TIMEOUT_S / 60)) minutes"; exit 1; }
  sleep 15
done
say "All three tiers have data after $(( $(date +%s) - start )) s"

echo
echo "One search per tier, for the newest partition in that tier:"
sql "SELECT tier, f, t FROM (
       SELECT DISTINCT ON (tier) tier,
              to_char(bucket_start AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS f,
              to_char(bucket_end AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS t
         FROM data_partitions WHERE state <> 'DELETED' ORDER BY tier, bucket_start DESC) newest
     ORDER BY array_position(ARRAY['HOT', 'WARM', 'COLD'], tier::text)" |
while read -r tier from to; do
  url="$ROUTER/api/v1/search?from=$from&to=$to&q=neptune&limit=5"
  curl -s "$url" | python3 -c '
import json, sys
d = json.load(sys.stdin)
t = d["tiers"][0]
print("  %-4s  %d hits in %d ms (%s)   curl \"%s\"" % (sys.argv[1], t["hits"], t["took_ms"], d["status"], sys.argv[2]))' "$tier" "$url"
done

cat <<TXT

Dashboards (no login needed to view):
  System health        $GRAFANA/d/obs-system-health
  Storage and search   $GRAFANA/d/obs-storage-search
  Security events      $GRAFANA/d/obs-security-events

Next steps for the demo:
  make test-failure-dlm-crash        kill dlm during a WARM to COLD move while searches run
  make clean up                      go back to the default policy and an empty stack
TXT
