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

# Builds a workshop container image via ko into Artifact Registry and records its
# digest-pinned reference in .workshop/<state-key>.
source "$(dirname "$0")/lib.sh"
load_env

target="${1:-}"
case "$target" in
  agent)
    cmd_dir="./cmd/agent"
    state_key="agent-image"
    ;;
  sre-gateway|gateway)
    cmd_dir="./cmd/sre-gateway"
    state_key="gateway-image"
    ;;
  *)
    die "usage: build.sh agent|sre-gateway [--check-changed]"
    ;;
esac

# Automatically reconcile Go imports in app/cmd/agent/main.go when building the
# agent so participants only need to replace buildRunner without editing imports.
sync_agent_imports() {
  local main_go="$REPO_ROOT/app/cmd/agent/main.go"
  [[ -f "$main_go" ]] || return 0
  if [[ "${1:-}" == "--check-changed" ]] && grep -Eq '^[[:space:]]*return[[:space:]]+nil,[[:space:]]*nil([[:space:]]|$)' "$main_go"; then
    die "app/cmd/agent/main.go still returns 'nil, nil' in buildRunner (starter stub). Paste the buildRunner replacement in Cloud Shell Editor, save the file (Ctrl+S / Cmd+S), and run make build-agent again."
  fi
  local body
  body="$(awk '/^func main\(\)/{flag=1} flag{print}' "$main_go")"
  local -a extra_imports=(
    '"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/agentruntime"'
  )
  [[ "$body" == *"brokerclient."* ]] && extra_imports+=('"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/brokerclient"')
  extra_imports+=('"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgerverify"')
  [[ "$body" == *"agent."* ]] && extra_imports+=('"google.golang.org/adk/v2/agent"')
  [[ "$body" == *"llmagent."* ]] && extra_imports+=('"google.golang.org/adk/v2/agent/llmagent"')
  [[ "$body" == *"plugin."* ]] && extra_imports+=('"google.golang.org/adk/v2/plugin"')
  extra_imports+=('"google.golang.org/adk/v2/runner"')
  [[ "$body" == *"session."* ]] && extra_imports+=('"google.golang.org/adk/v2/session"')
  extra_imports+=('"google.golang.org/adk/v2/tool"')
  [[ "$body" == *"loadmemorytool."* ]] && extra_imports+=('"google.golang.org/adk/v2/tool/loadmemorytool"')
  [[ "$body" == *"preloadmemorytool."* ]] && extra_imports+=('"google.golang.org/adk/v2/tool/preloadmemorytool"')
  [[ "$body" == *"skilltoolset."* ]] && extra_imports+=('"google.golang.org/adk/v2/tool/skilltoolset"')

  local imports_block=$'import (\n\t"context"\n\t"encoding/json"\n\t"fmt"\n\t"log"\n\t"net/http"\n\t"os"\n\t"path/filepath"\n\t"strings"\n'
  local imp
  for imp in "${extra_imports[@]}"; do
    imports_block+=$'\t'"${imp}"$'\n'
  done
  imports_block+=")"

  IMPORT_REPLACEMENT="$imports_block" awk '
    /^import \(/ { in_imp=1; print ENVIRON["IMPORT_REPLACEMENT"]; next }
    in_imp && /^\)/ { in_imp=0; next }
    !in_imp { print }
  ' "$main_go" > "${main_go}.tmp"
  if command -v gofmt >/dev/null 2>&1; then
    gofmt -w "${main_go}.tmp" 2>/dev/null || true
  fi
  if ! cmp -s "${main_go}.tmp" "$main_go"; then
    cat "${main_go}.tmp" > "$main_go"
  fi
  rm -f "${main_go}.tmp"
}

if [[ "$target" == "agent" ]]; then
  sync_agent_imports "${2:-}"
fi

export KO_DOCKER_REPO KO_DEFAULTPLATFORMS
step "ko build ${cmd_dir#./cmd/} -> ${KO_DOCKER_REPO}"
prev="$(state_get "$state_key" || true)"
rm -f "$STATE_DIR/$state_key"
if [[ "$target" == "agent" ]]; then
  rm -f "$STATE_DIR/agent-image-features"
fi
ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths "$cmd_dir")"
[[ "$ref" == *@sha256:* ]] || die "ko did not return a digest-pinned image reference: $ref"
state_put "$state_key" "$ref"
if [[ "$state_key" == "gateway-image" ]]; then
  state_put "gateway-image-built" "$ref"
fi
if [[ "$target" == "agent" ]]; then
  main_go="$REPO_ROOT/app/cmd/agent/main.go"
  features=""
  for sym in NewModel NewSessionService NewMemoryService NewSkillSource NewRemediationTools DiscoverMCPToolsets; do
    if grep -q "${sym}(" "$main_go" 2>/dev/null; then
      features+="${sym},"
    fi
  done
  state_put "agent-image-features" "$features"
fi
ok "$ref (saved to .workshop/$state_key)"

if [[ "${2:-}" == "--check-changed" && "$ref" == "$prev" ]]; then
  warn "image digest matches your previous ${cmd_dir#./cmd/} build. If this step asked you to edit app/cmd/agent/main.go, complete its WORKSHOP(...) section, save the file (Ctrl+S / Cmd+S), and run make build-agent again."
fi
