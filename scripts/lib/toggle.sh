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
  local value="${!var_name-}"
  case "${value,,}" in
    ''|true)
      echo true
      ;;
    false)
      echo false
      ;;
    *)
      echo "WARNING: unrecognized value '${value}' for ${var_name}; treating as enabled" >&2
      echo true
      ;;
  esac
}
