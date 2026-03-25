#!/usr/bin/env bash
# Prepare the OpenShift client tarball for a local podman build.
# CI: Konflux mounts Hermeto prefetch output at /cachi2/output/deps/generic/.
# Local: run this script, then build with deps mounted at that path:
#   podman build -v "$(pwd)/deps:/cachi2/output/deps:Z" -t segment-bridge .
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUT_DIR="$REPO_ROOT/deps/generic"
LOCK_FILE="$REPO_ROOT/artifacts.lock.yaml"

HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
  x86_64)        OC_ARCH="amd64" ;;
  aarch64|arm64) OC_ARCH="arm64" ;;
  *) echo "Unsupported architecture: $HOST_ARCH" >&2; exit 1 ;;
esac

# Read URL, checksum, and filename from the lock file (maintained by Renovate)
URL=$(grep "download_url:.*linux-${OC_ARCH}-rhel9" "$LOCK_FILE" \
  | sed 's/.*download_url: *"\(.*\)"/\1/')
EXPECTED_SHA=$(grep -A1 "download_url:.*linux-${OC_ARCH}-rhel9" "$LOCK_FILE" \
  | grep "checksum:" | sed 's/.*sha256:\([a-f0-9]*\).*/\1/')
TARBALL_NAME=$(grep -A2 "download_url:.*linux-${OC_ARCH}-rhel9" "$LOCK_FILE" \
  | grep "filename:" | sed 's/.*filename: *"\(.*\)"/\1/')

if [[ -z "$URL" || -z "$EXPECTED_SHA" || -z "$TARBALL_NAME" ]]; then
  echo "Failed to parse ${OC_ARCH} artifact from $LOCK_FILE" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
OUT_FILE="$OUT_DIR/$TARBALL_NAME"

if [[ -f "$OUT_FILE" ]]; then
  if echo "$EXPECTED_SHA  $OUT_FILE" | sha256sum -c - --quiet 2>/dev/null; then
    echo "Already present and valid: $OUT_FILE"
    exit 0
  fi
  echo "Removing stale $OUT_FILE"
  rm -f "$OUT_FILE"
fi

echo "Downloading OpenShift client tarball to $OUT_FILE ..."
curl -sSL -o "$OUT_FILE" "$URL"
echo "Verifying checksum..."
echo "$EXPECTED_SHA  $OUT_FILE" | sha256sum -c -
echo "Done. Run: podman build -v \"\$(pwd)/deps:/cachi2/output/deps:Z\" -t segment-bridge ."
