# DESIGN.md — Google Cloud Console Design System (`sre-gateway` UI)

Visual, spatial, typographic, and interaction specification for the workshop web interface served by `sre-gateway`.

Unlike standalone consumer chat apps or dark glassmorphic stage dashboards, this interface is modeled directly after **Google Cloud Console** (**Cloud Design System** / Pantheon GM2 Light & Dark tokens and **Gemini Cloud Assist Investigations**). It feels like an authentic first-party Google Cloud Console operational view in both Light Mode and Dark Mode.

---

## 1. Core Visual Principles

1. **Authentic Cloud Console Chrome & Density**: Crisp architectural precision built on an `8px` spatial grid (`4px` half-steps), `13px / 20px` standard body density, flat tonal surfaces, and `1px solid` hairline dividers — never blurred glassmorphism, neon gradients, or oversized marketing hero cards.
2. **Strict Sentence Case**: Every heading, navigation item, tab, table column header, chip, badge, and button label uses **sentence case** (e.g., `Approve remediation`, `Session ledger`, `Skill & agent catalog`, `Suspended · 0 vCPU`). Capitalize only the first word, proper nouns (`Google Cloud`, `GKE`, `Agent Substrate`, `Gemini`, `Model Armor`), and initialisms (`MCP`, `A2A`, `SPIFFE`, `OTel`, `RAM`, `CPU`, `SHA-256`, `3LO`). Never use `Title Case` or `text-transform: uppercase`.
3. **SVG Iconography Only**: Use crisp `18px`–`20px` inline SVG icons aligned to Pantheon / Google Symbols geometry (`check_circle`, `warning`, `error`, `refresh`, `play_arrow`, `pause_circle`, `shield`, `terminal`, `memory`, `account_tree`, `dark_mode`, `light_mode`). Never use emoji icons in UI chrome.
4. **Zero-Layout-Shift Light & Dark Theme Toggle**: Default to **Cloud Console Light Mode** (`data-theme="light"`), with an icon-only segmented Moon/Sun toggle (`aria-label` + tooltip, zero text label) at the top-right of the `48px` Console Platform Bar. Switching themes overrides **only** CSS color custom properties (`--cm-sys-color-*`) with zero changes to font sizes, padding, borders, dimensions, or DOM layout.

---

## 2. Color Tokens (Cloud Design System Light & Dark Contexts)

All component rules MUST consume `--cm-sys-color-*` custom properties and never hardcode hex literals.

| Semantic Role | CSS Custom Property | Light Mode (Default) | Dark Mode (`[data-theme="dark"]`) | Usage |
| :--- | :--- | :--- | :--- | :--- |
| **Canvas / Base Surface** | `--cm-sys-color-surface` | `#ffffff` | `#202124` | Main page canvas, primary detail panels, chat stream background |
| **Page Backdrop / Neutral Container** | `--cm-sys-color-neutral-container` | `#f1f3f4` | `#2a2b2e` | Secondary workspaces, table headers, filter bars, code block headers |
| **Surface Elevation 1** | `--cm-sys-color-surface-elevation-1` | `#ffffff` | `#2a2b2e` | Console cards, resource tables, docked Cloud Assist panel |
| **Surface Elevation 2** | `--cm-sys-color-surface-elevation-2` | `#ffffff` | `#303134` | Top `48px` Platform Bar, dropdown menus, popovers |
| **Surface Elevation 3** | `--cm-sys-color-surface-elevation-3` | `#ffffff` | `#36373a` | Modal dialogs (`⌘K` operator tools), slide-over drawers |
| **Primary Text** | `--cm-sys-color-on-surface` | `#202124` | `#e8eaed` | Headings, primary body copy, table values |
| **Secondary / Metadata Text** | `--cm-sys-color-on-surface-variant` | `#5f6368` | `#bdc1c6` | Timestamps, labels, captions, secondary icons |
| **Primary Action** | `--cm-sys-color-primary` | `#1a73e8` | `#8ab4f8` | Flat primary button fill, active tab indicator, links |
| **On Primary** | `--cm-sys-color-on-primary` | `#ffffff` | `#202124` | Foreground text/icon on `--cm-sys-color-primary` |
| **Primary / Selection Container** | `--cm-sys-color-selection-container` | `#e8f0fe` | `#394457` | Selected left-nav item, active filter chip, informational callout |
| **On Selection Container** | `--cm-sys-color-on-selection-container` | `#1967d2` | `#8ab4f8` | Foreground text/icon on `--cm-sys-color-selection-container` |
| **Hairline Divider** | `--cm-sys-color-hairline` | `#dadce0` | `#3c4043` | `1px solid` section borders, card borders, table row separators |
| **Interactive Outline** | `--cm-sys-color-outline` | `#80868b` | `#9aa0a6` | Stroked buttons, text inputs, composer border |
| **Active Outline** | `--cm-sys-color-outline-active` | `#1a73e8` | `#8ab4f8` | `2px` focus ring / active input border |
| **Status Success** | `--cm-sys-color-status-success` | `#188038` | `#81c995` | Healthy status icon, `Running` / `Verified` pill accent |
| **Status Success Container** | `--cm-sys-color-status-success-container` | `#e6f4ea` | `#37493f` | Positive banner & status badge background |
| **Status Warning** | `--cm-sys-color-status-warning` | `#e37400` | `#fdd663` | `Remediation gate` / `Parked` / caution icon & accent |
| **Status Warning Container** | `--cm-sys-color-status-warning-container` | `#fef7e0` | `#554c33` | Remediation approval banner & warning badge background |
| **Status Error** | `--cm-sys-color-status-error` | `#d93025` | `#f28b82` | Incident alert, Model Armor block, MCP `403` denial icon |
| **Status Error Container** | `--cm-sys-color-status-error-container` | `#fce8e6` | `#523a3b` | Critical alert banner & blocked policy card background |

---

## 3. Typography Scale

Uses **Roboto** for UI/prose and **Roboto Mono** for logs, diffs, resource names, SPIFFE IDs, SHA-256 digests, and trace spans (`font-variant-numeric: tabular-nums` on all metrics, latencies, and counters).

| Token | Family | Size / Line Height | Weight | Application |
| :--- | :--- | :--- | :--- | :--- |
| `title-large` | `Roboto, system-ui, sans-serif` | `20px / 28px` | `400` | Page `<h1>` in the Console Action Bar |
| `title-medium` | `Roboto, system-ui, sans-serif` | `16px / 24px` | `500` | Card `<h2>` headers, Cloud Assist panel header, modal title |
| `label-medium` | `Roboto, system-ui, sans-serif` | `13px / 20px` | `500` | Buttons, tabs, left-nav items, table column headers |
| `label-small` | `Roboto, system-ui, sans-serif` | `12px / 16px` | `500` | Status pills, metadata badges, chip labels |
| `body-medium` | `Roboto, system-ui, sans-serif` | `13px / 20px` | `400` | Standard prose, agent messages, table cells, descriptions |
| `body-small` | `Roboto, system-ui, sans-serif` | `12px / 16px` | `400` | Helper captions, timestamps, trace span subtitles |
| `code` | `'Roboto Mono', ui-monospace, monospace` | `12px / 20px` | `400` | Workspace diffs, diagnostic script stdout, CLI snippets, URNs |

---

## 4. Geometry, Spacing & Components

- **Spacing Grid**: `4px` (`--space-50`), `8px` (`--space-100`), `12px` (`--space-150`), `16px` (`--space-200`, default card inset and grid gutter), `24px` (`--space-300`, page padding).
- **Corner Radius**:
  - `4px` (`--radius-xs`): Buttons, inputs, Console cards, callout banners, code blocks, Cloud Assist prompt box.
  - `8px` (`--radius-sm`): Popovers, menus, modal dialog (`⌘K`).
  - `9999px` (`--radius-full`): Status chips/badges (`Running`, `Suspended · 0 vCPU`, `Blocked by Model Armor`), round icon buttons.
- **Button Hierarchy**:
  - **Flat Primary** (`32px` height, `0 12px` padding, `4px` radius, `--cm-sys-color-primary` fill): Maximum **one** primary CTA per region (e.g., `Approve remediation`, `Send`).
  - **Stroked Secondary** (`32px` height, `1px solid var(--cm-sys-color-outline)`): Secondary actions (`Refresh`, `Inspect snapshot`, `Run online eval`).
  - **Text Button**: Low-emphasis actions (`Reject`, `Cancel`, `Clear`).

---

## 5. Console Shell & Path-Based Routes

The application uses clean path-based routing (never `?view=` query params) inside a persistent 3-zone **Google Cloud Console** shell:

1. **Global Console Platform Bar (`48px` top header, `Elevation 2` + bottom hairline)**:
   - Left: Collapsible nav menu icon + Google Cloud four-color mark + product title `Gemini Agent Platform · Agent Substrate` + stroked project selector pill (`${PROJECT_ID}`).
   - Right: Live Broker SPIFFE status indicator, discrete `⌘K` Operator & Scenario Controls icon button, and the icon-only **Moon / Sun Theme Toggle** (`light_mode` / `dark_mode`).
2. **Collapsible Left Section Navigation (`240px` expanded / `56px` rail, `1px` right hairline)**:
   - `/` — **Investigations** (Incident Console & Cloud Assist Triage Agent)
   - `/sessions` — **Session ledger & memory** (Zero-wake **Gemini Sessions** event viewer & **Gemini Memory Bank** consolidated facts)
   - `/catalog` — **Skill & agent catalog** (**Skill Registry** SHA-256 revisions, **Agent Registry** A2A Agent Card & MCP tool policies)
   - `/about` — **Architecture** (Interactive, animated Cloud Console-styled SVG topology diagram of the **Actor Boundary**, kept in sync with codebase architecture)
3. **Primary `/` Split Workspace (Cloud Investigations + Docked Cloud Assist Panel)**:
   - **Center Console Detail Canvas**:
     - Top **Action Bar** (`48px`): Incident selector (`INC-402` / `INC-409`), Incident Actor state badge (`Running on worker-a`, `Suspended · 0 vCPU · <400ms wake`), template badge (`sre-agent-v1..v4`), and `Refresh` / `Suspend actor` stroked controls.
     - Conditional **Remediation Gate Banner** (`--cm-sys-color-status-warning-container`): Appears when ADK `toolconfirmation` pauses the turn and suspends the Actor to GCS, showing the staged patch summary + `Approve remediation` (Flat Primary) and `Reject` (Text) buttons, or 3LO **Auth Manager** consent card when operator delegation is required.
     - **Console Detail Tabs**: `Overview & workspace diff` (live `/workspace/ledger-api` git status & staged patch), `Governance & policy log` (Model Armor verdicts, MCP `readOnlyHint`/`destructiveHint` checks, keyless snapshot verification), and `Trace & online evals` (`gen_ai.*` waterfall + autorater scores).
   - **Right-Docked SRE Triage Agent Panel (`420px` width, expandable, modeled after Gemini Cloud Assist)**:
     - Clean header (`SRE triage agent` + session ID), scrollable turn stream with progressive collapsible disclosure rows for tool/skill calls (`load_skill`, `run_workspace_command`, MCP queries), starter investigation chips when empty, and bottom `4px`-radius prompt composer.
