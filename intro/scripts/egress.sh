#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# make egress POLICY=<file> ACTOR=<name> | ALL=1 — applies an EgressPolicy with egressctl.
source "$(dirname "$0")/ate-api.sh"
[[ -x "$REPO_ROOT/bin/egressctl" ]] || (cd "$REPO_ROOT/app" && go build -o ../bin/egressctl ./cmd/egressctl)
cmd="${1:?usage: egress.sh apply|get|delete [file] --actor=NAME|--all}"; shift
case "$cmd" in
  apply) file="${1:?policy file required}"; shift; [[ -f "$file" ]] || die "no such policy file: $file"
         egressctl -a "$ATESPACE" apply -f "$file" "$@" ;;
  *)     egressctl -a "$ATESPACE" "$cmd" "$@" ;;
esac
