#!/bin/bash
# prepare-input.sh
#   Convert all YAML files in sample/input/ to a single NDJSON file (input.json).
#   Output format: one compact JSON object per line, suitable for scripts/tekton-to-segment.sh
#   (which reads one record per line from stdin).
#
#   Requires: yq (https://github.com/mikefarah/yq) and jq.
#   Usage: run from repo root or any dir; paths are relative to this script.
#
set -o pipefail -o nounset

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INPUT_DIR="${SCRIPT_DIR}/input"
OUTPUT_FILE="${SCRIPT_DIR}/input.json"

if ! command -v yq &>/dev/null; then
  echo "prepare-input.sh: error: yq is required but not installed. See https://github.com/mikefarah/yq" >&2
  exit 1
fi
if ! command -v jq &>/dev/null; then
  echo "prepare-input.sh: error: jq is required but not installed." >&2
  exit 1
fi

# Normalize JSON key order so output is deterministic across yq/jq versions
SORT_KEYS='def sort_keys: if type == "object" then to_entries | sort_by(.key) | map({key: .key, value: (.value | sort_keys)}) | from_entries elif type == "array" then map(sort_keys) else . end; sort_keys'

# Overwrite output; we append one line per YAML file
: > "$OUTPUT_FILE"
count=0
skipped=0

for f in $(find "$INPUT_DIR" -maxdepth 1 -name '*.yaml' -print | LC_ALL=C sort); do
  line=$(yq eval -o=json '.' -- "$f" 2>/dev/null | jq -c "$SORT_KEYS" 2>/dev/null) || true
  if [[ -z "$line" ]]; then
    echo "prepare-input.sh: warning: skipping (empty or invalid YAML): $f" >&2
    ((skipped++)) || true
    continue
  fi
  echo "$line" >> "$OUTPUT_FILE"
  ((count++)) || true
done

echo "prepare-input.sh: wrote $count records to $OUTPUT_FILE (skipped $skipped files)" >&2

