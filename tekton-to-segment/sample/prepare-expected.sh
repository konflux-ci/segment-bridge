#!/bin/bash
# prepare-expected.sh
#   Convert all YAML files in sample/expected/ to a single NDJSON file (expected.json).
#   Each file may contain multiple YAML documents (---); one compact JSON line per document.
#   Output is normalized with sort_keys so it matches the generator output exactly.
#
#   Requires: yq (https://github.com/mikefarah/yq) and jq.
#   Usage: run from repo root or any dir; paths are relative to this script.
#
set -o pipefail -o nounset

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXPECTED_DIR="${SCRIPT_DIR}/expected"
OUTPUT_FILE="${SCRIPT_DIR}/expected.json"

SORT_KEYS='def sort_keys: if type == "object" then to_entries | sort_by(.key) | map({key: .key, value: (.value | sort_keys)}) | from_entries elif type == "array" then map(sort_keys) else . end; sort_keys'

if ! command -v yq &>/dev/null; then
  echo "prepare-expected.sh: error: yq is required but not installed. See https://github.com/mikefarah/yq" >&2
  exit 1
fi
if ! command -v jq &>/dev/null; then
  echo "prepare-expected.sh: error: jq is required but not installed." >&2
  exit 1
fi

if [[ ! -d "$EXPECTED_DIR" ]]; then
  echo "prepare-expected.sh: error: expected directory not found: $EXPECTED_DIR" >&2
  exit 1
fi

: > "$OUTPUT_FILE"
count=0

for f in $(find "$EXPECTED_DIR" -maxdepth 1 -name '*.yaml' -print | LC_ALL=C sort); do
  indices=$(yq eval-all 'di' -- "$f" 2>/dev/null) || true
  for i in $indices; do
    line=$(yq eval-all "select(di == $i) | ." -o=json -- "$f" 2>/dev/null | jq -c "$SORT_KEYS" 2>/dev/null) || true
    if [[ -z "$line" ]]; then
      echo "prepare-expected.sh: warning: skipping empty doc $i in $f" >&2
      continue
    fi
    echo "$line" >> "$OUTPUT_FILE"
    ((count++)) || true
  done
done

echo "prepare-expected.sh: wrote $count records to $OUTPUT_FILE" >&2
