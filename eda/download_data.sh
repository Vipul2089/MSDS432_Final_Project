#!/usr/bin/env bash
# Downloads the raw datasets used by the EDA notebooks into data/raw/ and
# records their SHA-256 checksums. Safe to re-run: existing files are kept.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RAW="${ROOT}/data/raw"

# NSL-KDD: the UNB download page requires a form, so we use a widely used
# GitHub mirror of the same files; row counts are checked by 01_nslkdd_eda.ipynb.
NSLKDD_BASE="https://raw.githubusercontent.com/defcom17/NSL_KDD/master"
CLOUDTRAIL_URL="https://summitroute.com/downloads/flaws_cloudtrail_logs.tar"

fetch() {
  local url="$1" dest="$2"
  if [[ -s "${dest}" ]]; then
    echo "exists: ${dest#"${ROOT}/"}"
  else
    echo "download: ${url}"
    curl --fail --silent --show-error --location --output "${dest}.tmp" "${url}"
    mv "${dest}.tmp" "${dest}"
  fi
}

mkdir -p "${RAW}/nslkdd" "${RAW}/cloudtrail"

fetch "${NSLKDD_BASE}/KDDTrain%2B.txt" "${RAW}/nslkdd/KDDTrain+.txt"
fetch "${NSLKDD_BASE}/KDDTest%2B.txt" "${RAW}/nslkdd/KDDTest+.txt"

fetch "${CLOUDTRAIL_URL}" "${RAW}/cloudtrail/flaws_cloudtrail_logs.tar"
if [[ ! -d "${RAW}/cloudtrail/flaws_cloudtrail_logs" ]]; then
  tar -xf "${RAW}/cloudtrail/flaws_cloudtrail_logs.tar" -C "${RAW}/cloudtrail"
fi

(
  cd "${RAW}"
  shasum -a 256 nslkdd/*.txt cloudtrail/flaws_cloudtrail_logs.tar > SHA256SUMS
)
echo "checksums written to data/raw/SHA256SUMS"
