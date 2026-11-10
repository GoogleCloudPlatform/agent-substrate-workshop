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

# Verifies AGENTS.md invariants on qwiklabs/en.md, diagram sync against modules/img/, and the
# pre-warmed contract assumed by CloudVLab/gcp-devrel-content (labs/gke004-kubecon-2026).
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DOC="$ROOT/qwiklabs/en.md"
fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }

# Markdown & Qwiklabs bundle invariants (AGENTS.md)
check "qwiklabs/en.md starts with an H1 heading (no YAML frontmatter)" \
  "[[ \"\$(head -n1 '$DOC')\" == '# '* ]]"
check "qwiklabs/en.md has no claat Duration: lines" \
  "! grep -qE '^Duration:' '$DOC'"
check "qwiklabs/en.md has no optional-lab appendix, references, or paths" \
  "! grep -qiE '^## Appendix: Optional content|optional lab|optional/[A-J]-' '$DOC'"
check "qwiklabs/en.md omits ### Clean up" \
  "! grep -qE '^### Clean up' '$DOC'"
check "qwiklabs/en.md omits Next: navigation lines" \
  "! grep -qE '^Next: \[' '$DOC'"

# Links outside fenced code blocks must be absolute https:// URLs or img/*.svg references.
bad_links="$(python3 - "$DOC" <<'PY'
import re, sys
with open(sys.argv[1], encoding="utf-8") as f:
    txt = re.sub(r"```.*?```", "", f.read(), flags=re.S)
    txt = re.sub(r"<ql-code-block[^>]*>.*?</ql-code-block>", "", txt, flags=re.S)
for m in re.finditer(r"!?\[[^\]]*\]\(([^)]+)\)", txt):
    target = m.group(1)
    if not (target.startswith("https://") or re.fullmatch(r"img/[^/]+\.svg", target)):
        print(target)
PY
)"
# shellcheck disable=SC2034 # bad_links is read inside the eval'd check() string below
check "qwiklabs/en.md uses only https:// links and img/*.svg diagrams" "[[ -z \"\$bad_links\" ]]"

# Referenced diagrams must exist in qwiklabs/img/ and match modules/img/.
referenced_imgs="$(python3 - "$DOC" <<'PY'
import re, sys
with open(sys.argv[1], encoding="utf-8") as f:
    txt = re.sub(r"```.*?```", "", f.read(), flags=re.S)
imgs = sorted(set(re.findall(r"!\[[^\]]*\]\((img/[^)]+\.svg)\)", txt)))
print("\n".join(imgs))
PY
)"
check "qwiklabs/en.md references at least 5 diagrams" \
  "(( \$(printf '%s\n' \"\$referenced_imgs\" | grep -c .) >= 5 ))"
while IFS= read -r rel; do
  [[ -z "$rel" ]] && continue
  base="${rel#img/}"
  check "diagram $base exists in qwiklabs/img/ and matches modules/img/" \
    "[[ -f '$ROOT/qwiklabs/img/$base' && -f '$ROOT/modules/img/$base' ]] && cmp -s '$ROOT/qwiklabs/img/$base' '$ROOT/modules/img/$base'"
done <<<"$referenced_imgs"

# Pre-warmed Qwiklabs flow & cross-repo contract (GKE004 bootstrap-substrate.sh & Task 1)
check "qwiklabs/en.md references .prewarm/workshop-prewarm.tar.gz and kubectl ate --help" \
  "grep -qF '.prewarm/workshop-prewarm.tar.gz' '$DOC' && grep -qF 'kubectl ate --help' '$DOC'"
check "qwiklabs/en.md runs make verify MODULE=0 and fallback MODULE=1 in Setup" \
  "grep -qF 'make verify MODULE=0' '$DOC' && grep -qF 'make catch-up MODULE=1' '$DOC'"
check "qwiklabs/en.md keeps Architecture tour and Hello actor as full hands-on sections" \
  "! grep -qiF 'Optional walkthrough' '$DOC' &&
   grep -qF 'WORKSHOP(module-2)' '$DOC' &&
   grep -qF 'make build-agent' '$DOC' &&
   grep -qF 'kubectl ate create atespace workshop' '$DOC' &&
   grep -qF 'kubectl ate create actor hello --template=agent-v1 -a workshop' '$DOC'"

substrate_ref="$(sed -n 's/^SUBSTRATE_REF=//p' "$ROOT/workshop.env.example")"
gomod_substrate="$(awk '$1 == "go.ate.dev/substrate" { print $2 }' "$ROOT/app/go.mod")"
# shellcheck disable=SC2034 # substrate_ref and gomod_substrate are read inside eval'd check()
check "workshop.env.example SUBSTRATE_REF matches app/go.mod go.ate.dev/substrate" \
  "[[ -n \"\$substrate_ref\" && \"\$substrate_ref\" == \"\$gomod_substrate\" ]]"

check "workshop.env.example keeps pre-warm resource defaults (bucket, AR repo, namespace, atespace)" \
  "grep -qxF 'BUCKET_NAME=\${PROJECT_ID}-ate' '$ROOT/workshop.env.example' &&
   grep -qxF 'AR_REPO=workshop' '$ROOT/workshop.env.example' &&
   grep -qxF 'NAMESPACE=workshop' '$ROOT/workshop.env.example' &&
   grep -qxF 'ATESPACE=workshop' '$ROOT/workshop.env.example'"

check "app/cmd/egressctl/main.go exists for GKE004 prewarm build" \
  "[[ -f '$ROOT/app/cmd/egressctl/main.go' ]]"

exit "$fails"
