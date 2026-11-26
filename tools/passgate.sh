#!/usr/bin/env bash
set -euo pipefail

passgate() {
  local -a needles=() cmd=()
  local sep=0 a
  for a in "$@"; do
    if [ "$sep" -eq 0 ]; then
      if [ "$a" = "--" ]; then sep=1; else needles+=("$a"); fi
    else
      cmd+=("$a")
    fi
  done
  local out s n diagnostic nested=${RUKIE_PASSGATE_ACTIVE:-0}
  out=$(mktemp) || return 1
  if RUKIE_PASSGATE_ACTIVE=1 "${cmd[@]}" >"$out" 2>&1; then s=0; else s=$?; fi
  cat "$out"
  if [ "$s" -ne 0 ]; then
    echo "passgate: exit $s" >&2
    if [ "${GITHUB_ACTIONS:-}" = true ] && [ "$nested" = 0 ]; then
      if ! diagnostic=$(grep -E -A8 '(^FAIL|--- FAIL:|\.go:[0-9]+:|^[Ee]rror|^make.*\*\*\*)' "$out" | tail -c 60000); then
        diagnostic=$(tail -c 60000 "$out")
      fi
      diagnostic=${diagnostic//%/%25}
      diagnostic=${diagnostic//$'\r'/%0D}
      diagnostic=${diagnostic//$'\n'/%0A}
      printf '::error title=Required check failed::%s\n' "$diagnostic"
    fi
    rm -f "$out"
    return "$s"
  fi
  for n in "${needles[@]}"; do
    if ! grep -Fq -- "$n" "$out"; then echo "passgate: no PASS line for: $n" >&2; rm -f "$out"; return 1; fi
  done
  rm -f "$out"
}

passgate "$@"
