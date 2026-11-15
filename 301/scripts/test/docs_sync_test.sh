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

# Offline multi-target publishing, SVG hygiene, and doc synchronization contract tests
# across modules/00..07/README.md, codelabs/en.md, qwiklabs/en.md, and instructor/*.md.
# shellcheck disable=SC2329 # t_* test functions are invoked dynamically via check()
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 1
export LC_ALL=C

# Single base-URL constant for repository links in codelabs/en.md and qwiklabs/en.md.
# When migrating Track 301 into GoogleCloudPlatform/agent-substrate-workshop/301, update
# REPO_BASE_URL to https://github.com/GoogleCloudPlatform/agent-substrate-workshop and
# REPO_PATH_PREFIX to /301.
REPO_BASE_URL="${REPO_BASE_URL:-https://github.com/GoogleCloudPlatform/agent-substrate-workshop}"
REPO_PATH_PREFIX="${REPO_PATH_PREFIX:-/301}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() {
  local name="$1"
  shift
  if "$@"; then
    echo "ok   $name"
  else
    echo "FAIL $name"
    fails=$((fails + 1))
  fi
}

EXPECTED_SVGS=(
  "01-actor-boundary.svg"
  "02-sessions-and-memory.svg"
  "03-skill-registry.svg"
  "04-remediation-gate.svg"
  "05-zero-trust-governance.svg"
  "06-observability-flywheel.svg"
)

scan_inline_backtick_angles() {
  python3 - "$@" <<'PY'
import re, sys

bad = 0
for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()
    in_fence = False
    in_ql_block = False
    for lineno, line in enumerate(lines, 1):
        stripped = line.lstrip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if stripped.startswith("<ql-code-block"):
            in_ql_block = True
            continue
        if stripped.startswith("</ql-code-block>"):
            in_ql_block = False
            continue
        if in_fence or in_ql_block:
            continue
        for match in re.finditer(r"(?<!`)`([^`\n]+)`(?!`)", line):
            span = match.group(1)
            if re.search(r"<[^>]*>", span):
                sys.stderr.write(f"  claat-unsafe <...> in inline code at {path}:{lineno}: {match.group(0)}\n")
                bad = 1
sys.exit(bad)
PY
}

t_codelabs_frontmatter_and_structure() {
  python3 - "$ROOT/codelabs/en.md" <<'PY'
import re, sys

with open(sys.argv[1], encoding="utf-8") as fh:
    text = fh.read()
lines = text.splitlines()

if not lines or lines[0] != "---":
    sys.stderr.write("  codelabs/en.md must start with --- YAML frontmatter\n")
    sys.exit(1)

end_idx = None
for idx in range(1, min(len(lines), 30)):
    if lines[idx] == "---":
        end_idx = idx
        break
if end_idx is None:
    sys.stderr.write("  codelabs/en.md missing closing --- frontmatter delimiter\n")
    sys.exit(1)

fm = "\n".join(lines[1:end_idx])
required_fm = [
    r"^id:\s*agent-substrate-301\s*$",
    r"^summary:\s*.+$",
    r"^categories:\s*AgentPlatform,\s*Kubernetes\s*$",
    r"language:GoLang",
    r"product:AgentPlatform",
]
for pat in required_fm:
    if not re.search(pat, fm, flags=re.MULTILINE):
        sys.stderr.write(f"  codelabs/en.md frontmatter missing {pat}\n")
        sys.exit(1)

for bad_tag in ("product:GeminiEnterpriseAgentPlatform", "language:Go,", "language:Go\n"):
    if bad_tag in fm + "\n":
        sys.stderr.write(f"  unrecognized DevSite nomenclature tag in frontmatter: {bad_tag.strip()}\n")
        sys.exit(1)

in_fence = False
step_headers = []
for i, line in enumerate(lines[end_idx + 1:], start=end_idx + 2):
    if line.lstrip().startswith("```"):
        in_fence = not in_fence
        continue
    if in_fence:
        continue
    if line.startswith("Next:"):
        sys.stderr.write(f"  unexpected Next: line in codelabs/en.md:{i}\n")
        sys.exit(1)
    if line.startswith("## "):
        step_headers.append(line)
        nxt = lines[i] if i < len(lines) else ""
        if not re.match(r"^Duration:\s*\d{2}:\d{2}$", nxt):
            sys.stderr.write(f"  codelabs/en.md:{i} ({line}) not followed by Duration: MM:SS (got {nxt!r})\n")
            sys.exit(1)

expected_steps = [
    "## Introduction",
    "## Setup",
    "## Actor boundary and first triage turn",
    "## Session ledger and consolidated memory",
    "## Dynamic procedural skills with Skill Registry",
    "## Agent Registry discovery and zero-compute remediation gate",
    "## Zero-trust governance across the actor boundary",
    "## Observability, GenAI traces, and online evals",
    "## Wrap-up and production patterns",
    "## Appendix: Optional content",
]
if step_headers != expected_steps:
    sys.stderr.write(f"  codelabs/en.md steps mismatch:\n    got:  {step_headers}\n    want: {expected_steps}\n")
    sys.exit(1)

if "AGENT_TO_ANYWHERE" not in text or "ambient-swp-egress-overlay.yaml.tmpl" not in text:
    sys.stderr.write("  codelabs/en.md Appendix missing Ambient Networking + Secure Web Proxy overlay\n")
    sys.exit(1)

h3_headers = [ln for ln in lines if ln.startswith("### ")]
if not h3_headers or h3_headers[-1] != "### When you're done":
    sys.stderr.write(f"  codelabs/en.md must end with '### When you're done' subsection (got {h3_headers[-1:]})\n")
    sys.exit(1)
PY
}

t_qwiklabs_conventions() {
  python3 - "$ROOT/qwiklabs/en.md" <<'PY'
import re, sys

with open(sys.argv[1], encoding="utf-8") as fh:
    text = fh.read()
lines = text.splitlines()

if not lines or not lines[0].startswith("# "):
    sys.stderr.write(f"  qwiklabs/en.md must start directly with '# Title' (got {lines[:1]})\n")
    sys.exit(1)

in_fence = False
step_headers = []
for i, line in enumerate(lines, 1):
    stripped = line.lstrip()
    if stripped.startswith("```"):
        in_fence = not in_fence
        continue
    if not in_fence:
        if re.match(r"^Duration:\s*", line):
            sys.stderr.write(f"  forbidden Duration: line in qwiklabs/en.md:{i}\n")
            sys.exit(1)
        if line.startswith("Next:"):
            sys.stderr.write(f"  forbidden Next: line in qwiklabs/en.md:{i}\n")
            sys.exit(1)
        if line.startswith("## "):
            step_headers.append(line)

expected_steps = [
    "## Introduction",
    "## Setup",
    "## Actor boundary and first triage turn",
    "## Session ledger and consolidated memory",
    "## Dynamic procedural skills with Skill Registry",
    "## Agent Registry discovery and zero-compute remediation gate",
    "## Zero-trust governance across the actor boundary",
    "## Observability, GenAI traces, and online evals",
    "## Wrap-up and production patterns",
]
if step_headers != expected_steps:
    sys.stderr.write(f"  qwiklabs/en.md steps mismatch:\n    got:  {step_headers}\n    want: {expected_steps}\n")
    sys.exit(1)

forbidden_optional = [
    "Appendix: Optional content",
    "ambient-swp-egress-overlay",
    "AGENT_TO_ANYWHERE",
    "Secure Web Proxy",
    "Ambient Networking",
    "GCPEgressPolicy",
    "optional overlay",
    "optional lab",
    "(#9)",
    "gcloud beta container clusters create",
]
for needle in forbidden_optional:
    if needle.lower() in text.lower():
        sys.stderr.write(f"  forbidden optional/manual-cluster reference {needle!r} in qwiklabs/en.md\n")
        sys.exit(1)

required_ql = [
    '<ql-variable key="project_0.project_id" placeHolder="YOUR_PROJECT_ID"></ql-variable>',
    '<ql-variable key="project_0.default_region" placeHolder="us-central1"></ql-variable>',
    '<ql-variable key="project_0.default_zone" placeHolder="us-central1-a"></ql-variable>',
    '<ql-code-block bash templated noWrap>',
    '{{{project_0.project_id | YOUR_PROJECT_ID}}}',
    '{{{project_0.default_region | us-central1}}}',
    '{{{project_0.default_zone | us-central1-a}}}',
    '</ql-code-block>',
]
for needle in required_ql:
    if needle not in text:
        sys.stderr.write(f"  missing required Qwiklabs binding {needle!r} in qwiklabs/en.md\n")
        sys.exit(1)

if "### Clean up" in text or "make teardown" in text:
    sys.stderr.write("  forbidden ### Clean up or make teardown in qwiklabs/en.md\n")
    sys.exit(1)

for prewarm_str in (".prewarm/workshop-301-prewarm.tar.gz", "kubectl ate --help", "make verify MODULE=0", "make catch-up MODULE=1"):
    if prewarm_str not in text:
        sys.stderr.write(f"  missing pre-warm contract string {prewarm_str!r} in qwiklabs/en.md\n")
        sys.exit(1)
PY
}

t_inline_backtick_safety() {
  local md_files=(
    "$ROOT/README.md"
    "$ROOT/codelabs/en.md"
    "$ROOT/qwiklabs/en.md"
    "$ROOT/instructor/runbook.md"
    "$ROOT/instructor/troubleshooting.md"
    "$ROOT"/modules/*/README.md
  )
  scan_inline_backtick_angles "${md_files[@]}" || return 1

  # Negative check: verify scanner catches `<placeholder>` inside single backticks.
  printf 'Broken inline code: `Authorization: Bearer <token>` here.\n' >"$TMP/bad.md"
  if scan_inline_backtick_angles "$TMP/bad.md" 2>/dev/null; then
    echo "  scanner failed to catch synthetic inline <token> placeholder" >&2
    return 1
  fi
  return 0
}

t_links_rewritten_and_resolvable() {
  python3 - "$ROOT" "$REPO_BASE_URL" "$REPO_PATH_PREFIX" <<'PY'
import os, re, sys

root, base_url, prefix = sys.argv[1], sys.argv[2].rstrip("/"), sys.argv[3].rstrip("/")
blob_prefix = f"{base_url}/blob/main{prefix}/"
tree_prefix = f"{base_url}/tree/main{prefix}/"

for rel_doc, allow_step_anchors in (("codelabs/en.md", True), ("qwiklabs/en.md", False)):
    doc_path = os.path.join(root, rel_doc)
    doc_dir = os.path.dirname(doc_path)
    with open(doc_path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()

    in_fence = False
    in_ql_block = False
    for lineno, line in enumerate(lines, 1):
        stripped = line.lstrip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if stripped.startswith("<ql-code-block"):
            in_ql_block = True
            continue
        if stripped.startswith("</ql-code-block>"):
            in_ql_block = False
            continue
        if in_fence or in_ql_block:
            continue

        # Verify image embeds resolve locally under codelabs/img or qwiklabs/img.
        for img_m in re.finditer(r"!\[[^\]]*\]\(([^)]+)\)", line):
            img_target = img_m.group(1)
            if not img_target.startswith("img/") or not os.path.isfile(os.path.join(doc_dir, img_target)):
                sys.stderr.write(f"  invalid or missing image reference in {rel_doc}:{lineno}: {img_target}\n")
                sys.exit(1)

        # Verify non-image links never use relative file paths and resolve inside the repo.
        for link_m in re.finditer(r"(?<!!)\[[^\]]+\]\(([^)]+)\)", line):
            target = link_m.group(1).strip()
            if allow_step_anchors and re.match(r"^#[0-9]+$", target):
                continue
            if not target.startswith(("https://", "http://")):
                sys.stderr.write(f"  relative or disallowed link in {rel_doc}:{lineno}: {target}\n")
                sys.exit(1)
            if target.startswith(blob_prefix):
                repo_rel = target[len(blob_prefix):].split("#", 1)[0]
                if not os.path.isfile(os.path.join(root, repo_rel)):
                    sys.stderr.write(f"  broken repo blob URL in {rel_doc}:{lineno}: {target}\n")
                    sys.exit(1)
            elif target.startswith(tree_prefix):
                repo_rel = target[len(tree_prefix):].split("#", 1)[0]
                if not os.path.isdir(os.path.join(root, repo_rel)):
                    sys.stderr.write(f"  broken repo tree URL in {rel_doc}:{lineno}: {target}\n")
                    sys.exit(1)
PY
}

t_core_modules_synchronized() {
  python3 - "$ROOT" <<'PY'
import os, re, sys

root = sys.argv[1]

def parse_h2_sections(path: str) -> dict[str, str]:
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()
    # Skip YAML frontmatter if present
    idx = 0
    if lines and lines[0] == "---":
        idx = 1
        while idx < len(lines) and lines[idx] != "---":
            idx += 1
        idx += 1
    sections: dict[str, list[str]] = {}
    cur = None
    in_fence = False
    for line in lines[idx:]:
        if line.lstrip().startswith("```"):
            in_fence = not in_fence
        if not in_fence and line.startswith("## "):
            cur = line[3:].strip()
            sections[cur] = []
            continue
        if cur is not None:
            if not in_fence and re.match(r"^Duration:\s*\d{2}:\d{2}$", line):
                continue
            sections[cur].append(line)
    return {k: "\n".join(v).strip() for k, v in sections.items()}

def extract_code_blocks(text: str) -> list[str]:
    blocks = []
    cur = []
    in_fence = False
    for line in text.splitlines():
        if line.lstrip().startswith("```"):
            if in_fence:
                blocks.append("\n".join(cur).strip())
                cur = []
            in_fence = not in_fence
            continue
        if in_fence:
            # Normalize leading list indentation so module/codelab/qwiklabs blocks compare cleanly.
            cur.append(line.strip())
    return blocks

codelab_secs = parse_h2_sections(os.path.join(root, "codelabs/en.md"))
qwiklabs_secs = parse_h2_sections(os.path.join(root, "qwiklabs/en.md"))

module_map = [
    ("modules/01-actor-boundary/README.md", "Actor boundary and first triage turn"),
    ("modules/02-sessions-and-memory/README.md", "Session ledger and consolidated memory"),
    ("modules/03-skill-registry/README.md", "Dynamic procedural skills with Skill Registry"),
    ("modules/04-agent-registry-mcp-a2a/README.md", "Agent Registry discovery and zero-compute remediation gate"),
    ("modules/05-zero-trust-governance/README.md", "Zero-trust governance across the actor boundary"),
    ("modules/06-observability-and-evals/README.md", "Observability, GenAI traces, and online evals"),
]

for mod_rel, sec_title in module_map:
    if codelab_secs.get(sec_title) != qwiklabs_secs.get(sec_title):
        sys.stderr.write(f"  codelabs/en.md and qwiklabs/en.md diverged in section {sec_title!r}\n")
        sys.exit(1)
    with open(os.path.join(root, mod_rel), encoding="utf-8") as fh:
        mod_text = fh.read()
    mod_blocks = extract_code_blocks(mod_text)
    cl_blocks = extract_code_blocks(codelab_secs[sec_title])
    if mod_blocks != cl_blocks:
        sys.stderr.write(f"  code blocks in {mod_rel} out of sync with codelabs/en.md ({len(mod_blocks)} vs {len(cl_blocks)})\n")
        sys.exit(1)
PY
}

t_svg_parity_and_license_hygiene() {
  local bad=0 svg
  for svg in "${EXPECTED_SVGS[@]}"; do
    local m="$ROOT/modules/img/$svg"
    local c="$ROOT/codelabs/img/$svg"
    local q="$ROOT/qwiklabs/img/$svg"
    if [[ ! -f "$m" || ! -f "$c" || ! -f "$q" ]]; then
      echo "  missing SVG copy for $svg across modules/img, codelabs/img, or qwiklabs/img" >&2
      bad=1
      continue
    fi
    if ! cmp -s "$m" "$c" || ! cmp -s "$m" "$q"; then
      echo "  SVG drift detected across modules/img, codelabs/img, and qwiklabs/img for $svg" >&2
      bad=1
    fi
    if ! head -n 5 "$m" | grep -q "Copyright 2026 Google LLC" || ! head -n 15 "$m" | grep -q "Apache License, Version 2.0"; then
      echo "  missing Apache 2.0 XML header in $m" >&2
      bad=1
    fi
    if grep -Eiq "@font-face|data:font|woff2?|truetype|https://" "$m"; then
      echo "  forbidden @font-face, embedded font data, or external URL in $m" >&2
      bad=1
    fi
    if ! grep -Fq "Roboto" "$m"; then
      echo "  missing explicit Roboto system font-family in $m" >&2
      bad=1
    fi
    if ! python3 -c 'import sys, xml.etree.ElementTree as ET; ET.parse(sys.argv[1])' "$m" 2>/dev/null; then
      echo "  invalid XML in $m" >&2
      bad=1
    fi
    if ! grep -Fq "(img/$svg)" "$ROOT/codelabs/en.md" || ! grep -Fq "(img/$svg)" "$ROOT/qwiklabs/en.md"; then
      echo "  $svg not referenced in codelabs/en.md or qwiklabs/en.md" >&2
      bad=1
    fi
  done

  # Also ensure no extra unsynced SVGs exist in one directory only.
  local list_m list_c list_q
  list_m="$(cd "$ROOT/modules/img" && printf '%s\n' *.svg | sort)"
  list_c="$(cd "$ROOT/codelabs/img" && printf '%s\n' *.svg | sort)"
  list_q="$(cd "$ROOT/qwiklabs/img" && printf '%s\n' *.svg | sort)"
  if [[ "$list_m" != "$list_c" || "$list_m" != "$list_q" ]]; then
    echo "  SVG file inventory differs across modules/img, codelabs/img, and qwiklabs/img" >&2
    bad=1
  fi

  local idx=1 mod_readme
  for svg in "${EXPECTED_SVGS[@]}"; do
    mod_readme="$(printf '%s/modules/0%d-' "$ROOT" "$idx")"
    if ! grep -Fq "(../img/$svg)" "${mod_readme}"*/README.md; then
      echo "  missing ../img/$svg reference in Module 0${idx} README.md" >&2
      bad=1
    fi
    idx=$((idx + 1))
  done
  return "$bad"
}

t_instructor_and_oss_docs() {
  [[ -s "$ROOT/instructor/runbook.md" ]] &&
    [[ -s "$ROOT/instructor/troubleshooting.md" ]] &&
    [[ -s "$ROOT/CONTRIBUTING.md" || -s "$ROOT/../CONTRIBUTING.md" ]] &&
    grep -Fq "2m 45s" "$ROOT/instructor/runbook.md" &&
    grep -Fq "gcp_medium_extra" "$ROOT/instructor/runbook.md" &&
    grep -Fq "max_hot_labs" "$ROOT/instructor/runbook.md" &&
    grep -Fq "60-minute" "$ROOT/instructor/runbook.md" &&
    grep -Fq "ate-snapshot-transfer" "$ROOT/instructor/troubleshooting.md" &&
    grep -Fq "GoldenEgressPolicy" "$ROOT/instructor/troubleshooting.md" &&
    grep -Fq "MODEL_ARMOR_BLOCKED" "$ROOT/instructor/troubleshooting.md" &&
    grep -Fq "Contributor License Agreement" "$ROOT/CONTRIBUTING.md" &&
    grep -Fq "CONTRIBUTING.md" "$ROOT/README.md" &&
    grep -Fq "Vulnerability Rewards Program" "$ROOT/README.md" &&
    grep -Fq "docs_sync_test.sh" "$ROOT/AGENTS.md" &&
    grep -Fq "make teardown YES=1" "$ROOT/README.md" &&
    grep -Fq "make install-status" "$ROOT/README.md" &&
    grep -Fq "five signal flows" "$ROOT/modules/01-actor-boundary/README.md" &&
    python3 - "$ROOT/modules/07-wrap-up/README.md" "$ROOT/codelabs/en.md" "$ROOT/qwiklabs/en.md" <<'PY'
import sys

# modules/07-wrap-up/README.md and codelabs/en.md must keep Clean up after Checkpoint
for path in sys.argv[1:3]:
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    idx_verify7 = text.find("make verify MODULE=7")
    idx_cleanup = text.rfind("make teardown YES=1")
    if idx_verify7 < 0 or idx_cleanup < 0 or idx_verify7 > idx_cleanup:
        sys.stderr.write(f"  Module 07 Checkpoint must precede Clean up in {path}\n")
        sys.exit(1)

# qwiklabs/en.md must have Checkpoint and omit Clean up
ql_path = sys.argv[3]
with open(ql_path, encoding="utf-8") as fh:
    ql_text = fh.read()
if "make verify MODULE=7" not in ql_text:
    sys.stderr.write("  qwiklabs/en.md missing make verify MODULE=7\n")
    sys.exit(1)
if "### Clean up" in ql_text or "make teardown YES=1" in ql_text:
    sys.stderr.write("  qwiklabs/en.md must omit ### Clean up\n")
    sys.exit(1)
PY
}

check "codelabs/en.md frontmatter (AgentPlatform, GoLang), Duration headers, and Appendix" t_codelabs_frontmatter_and_structure
check "qwiklabs/en.md omits frontmatter, Duration, and optional overlays and binds ql-variables" t_qwiklabs_conventions
check "zero claat-unsafe <...> inline backtick spans across modular, codelab, and qwiklabs docs" t_inline_backtick_safety
check "codelabs/en.md and qwiklabs/en.md rewrite all relative repo links to valid GitHub URLs" t_links_rewritten_and_resolvable
check "Modules 01-06 content and code blocks stay synchronized across modules/, codelabs/, and qwiklabs/" t_core_modules_synchronized
check "SVG diagrams have Apache 2.0 headers, zero @font-face blocks, and 3-way byte parity" t_svg_parity_and_license_hygiene
check "instructor runbook, troubleshooting, CONTRIBUTING.md, README VRP, and AGENTS.md sync docs present" t_instructor_and_oss_docs
exit "$fails"
