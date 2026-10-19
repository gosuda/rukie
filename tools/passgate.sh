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
  local out s n
  out=$(mktemp) || return 1
  "${cmd[@]}" >"$out" 2>&1; s=$?
  cat "$out"
  if [ "$s" -ne 0 ]; then echo "passgate: exit $s" >&2; rm -f "$out"; return "$s"; fi
  for n in "${needles[@]}"; do
    if ! grep -Fq -- "$n" "$out"; then echo "passgate: no PASS line for: $n" >&2; rm -f "$out"; return 1; fi
  done
  rm -f "$out"
}

passgate "$@"
