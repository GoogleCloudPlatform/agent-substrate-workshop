#!/usr/bin/env bash
# make egress POLICY=<file> ACTOR=<name> | ALL=1 — applies an EgressPolicy with egressctl.
source "$(dirname "$0")/ate-api.sh"
[[ -x "$REPO_ROOT/bin/egressctl" ]] || (cd "$REPO_ROOT/app" && go build -o ../bin/egressctl ./cmd/egressctl)
cmd="${1:?usage: egress.sh apply|get|delete [file] --actor=NAME|--all}"; shift
case "$cmd" in
  apply) file="${1:?policy file required}"; shift; [[ -f "$file" ]] || die "no such policy file: $file"
         egressctl -a "$ATESPACE" apply -f "$file" "$@" ;;
  *)     egressctl -a "$ATESPACE" "$cmd" "$@" ;;
esac
