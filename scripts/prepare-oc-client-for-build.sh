#!/usr/bin/env bash
# Prepare the OpenShift client tarball for a local podman build.
# CI: Konflux mounts Hermeto prefetch output at /cachi2/output/deps/generic/.
# Local: run this script, then build with deps mounted at that path:
#   podman build -v "$(pwd)/deps:/cachi2/output/deps:Z" -t segment-bridge .
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUT_DIR="$REPO_ROOT/deps/generic"

HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
  x86_64)        OC_ARCH="amd64" ;;
  aarch64|arm64) OC_ARCH="arm64" ;;
  *) echo "Unsupported architecture: $HOST_ARCH" >&2; exit 1 ;;
esac

case "$OC_ARCH" in
  amd64) EXPECTED_SHA="b0724e3a4da96e39642d9b191eda711218e6ce5d362d183435cb6cfb9ff5d693" ;;
  arm64) EXPECTED_SHA="3b0b98238723dc1fc3c29c3ce1cbc23c826f77bcc4f481f3ea6235ddbd4a51bc" ;;
esac

TARBALL_NAME="openshift-client-linux-${OC_ARCH}-rhel9.tar.gz"
URL="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/stable/openshift-client-linux-${OC_ARCH}-rhel9-4.21.6.tar.gz"

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
