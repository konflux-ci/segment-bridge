#!/bin/bash
# toggle.sh
#   Shared env-var toggle helper sourced by tekton-main-job.sh and
#   tekton-to-segment.sh. Not executed directly.

# resolve_toggle: Print "true" or "false" for an env-var toggle.
# Arguments:
#   $1 - environment variable name
# Output:
#   "true" or "false" to stdout. Unset, empty, and "true" (any case)
#   enable the source. Only the literal value "false" (any case)
#   disables it. Unrecognized values fail open with a WARNING on stderr.
resolve_toggle() {
  local var_name="$1"
  local value restore_nocase
  value="${!var_name-}"
  shopt -q nocasematch || restore_nocase=1
  shopt -s nocasematch
  case "$value" in
    ''|true)
      echo true
      ;;
    false)
      echo false
      ;;
    *)
      echo "WARNING: unrecognized value '${value}' for ${var_name}; treating as enabled (use false to disable)" >&2
      echo true
      ;;
  esac
  if [ -n "${restore_nocase-}" ]; then
    shopt -u nocasematch
  fi
}
