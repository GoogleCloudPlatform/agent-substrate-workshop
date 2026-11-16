// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

(() => {
  "use strict";

  const SVG_NS = "http://www.w3.org/2000/svg";

  const ICON_PATHS = {
    menu: ["M3 6h18v2H3V6zm0 5h18v2H3v-2zm0 5h18v2H3v-2z"],
    light_mode: [
      "M12 7a5 5 0 1 0 0 10 5 5 0 0 0 0-10zm0-5h1v3h-2V2h1zm0 17h1v3h-2v-3h1zM4.22 5.64l1.42-1.42 2.12 2.12-1.42 1.42-2.12-2.12zm12.02 12.02 1.42-1.42 2.12 2.12-1.42 1.42-2.12-2.12zM2 11h3v2H2v-2zm17 0h3v2h-3v-2zM4.22 18.36l2.12-2.12 1.42 1.42-2.12 2.12-1.42-1.42zM16.24 6.34l2.12-2.12 1.42 1.42-2.12 2.12-1.42-1.42z"
    ],
    dark_mode: [
      "M12.3 4.9c.4-.2.6-.7.5-1.1-.1-.4-.5-.8-1-.8-4.9.1-8.8 4.1-8.8 9 0 5 4 9 9 9 3.8 0 7.1-2.4 8.4-5.9.2-.4 0-.9-.4-1.2-.4-.3-.9-.2-1.2.1-1 .9-2.3 1.4-3.7 1.4-3.1 0-5.6-2.5-5.6-5.6 0-2.1 1.1-4 2.8-4.9z"
    ],
    terminal: [
      "M4 5h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2zm0 2v10h16V7H4zm2.8 2.3L10.5 12l-3.7 2.7-1.2-1.6L7.8 12 5.6 10.9l1.2-1.6zM11 14h6v2h-6v-2z"
    ],
    memory: [
      "M9 3h2v2h2V3h2v2h2a2 2 0 0 1 2 2v2h2v2h-2v2h2v2h-2v2a2 2 0 0 1-2 2h-2v2h-2v-2h-2v2H9v-2H7a2 2 0 0 1-2-2v-2H3v-2h2v-2H3V9h2V7a2 2 0 0 1 2-2h2V3zm-2 4v10h10V7H7zm2 2h6v6H9V9z"
    ],
    shield: [
      "M12 2 4 5v6.1c0 5 3.4 9.7 8 10.9 4.6-1.2 8-5.9 8-10.9V5l-8-3zm0 2.2 6 2.2V11c0 4.1-2.7 8-6 9.1-3.3-1.1-6-5-6-9.1V6.4l6-2.2zm-1.1 10.4-2.5-2.5 1.4-1.4 1.1 1.1 3.7-3.7 1.4 1.4-5.1 5.1z"
    ],
    account_tree: [
      "M22 11V3h-7v3H9V3H2v8h7V8h2v10h4v3h7v-8h-7v3h-2V8h2v3h7zM7 9H4V5h3v4zm10 6h3v4h-3v-4zm0-10h3v4h-3V5z"
    ],
    refresh: [
      "M17.65 6.35A7.95 7.95 0 0 0 12 4a8 8 0 1 0 7.73 10h-2.08A6 6 0 1 1 12 6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"
    ],
    pause_circle: [
      "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20zm-1 14H9V8h2v8zm4 0h-2V8h2v8z"
    ],
    play_arrow: ["M8 5v14l11-7L8 5z"],
    warning: ["M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"],
    tune: [
      "M3 17v2h6v-2H3zM3 5v2h10V5H3zm10 16v-2h8v-2h-8v-2h-2v6h2zM7 9v2H3v2h4v2h2V9H7zm14 4v-2H11v2h10zm-6-4h2V7h4V5h-4V3h-2v6z"
    ]
  };

  const PROMPT_PRESETS = [
    {
      label: "Triage INC-402",
      incident: "INC-402",
      prompt: "Investigate INC-402 by cloning ledger-api into /workspace, inspecting config/pool.yaml and pool/store.go, and running ledger-verify."
    },
    {
      label: "Run skill diagnosis",
      incident: "INC-402",
      prompt: "Run the ledger-pool-triage procedural skill against INC-402 and report the active SKILL_REVISION findings."
    },
    {
      label: "Stage & remediate PR",
      incident: "INC-402",
      prompt: "Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."
    },
    {
      label: "Recall memory (INC-403)",
      incident: "INC-403",
      prompt: "Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."
    },
    {
      label: "Poisoned note (INC-409)",
      incident: "INC-409",
      prompt: "Investigate INC-409 and summarize the attached partner webhook diagnostic note."
    }
  ];

  const STARTER_PROMPTS = PROMPT_PRESETS.map((p) => p.prompt);

  const state = {
    csrfToken: "",
    sessionInfo: null,
    activeRoute: "/",
    activeTab: "overview",
    selectedIncidentID: "INC-402",
    incidents: [],
    targetStatus: null,
    actors: [],
    ledgerSessions: [],
    ledgerBackend: "broker-session-ledger",
    selectedLedgerSessionID: "",
    selectedLedgerDetail: null,
    memories: [],
    memoryQuery: "",
    catalogBackend: "broker-skill-registry",
    catalogAgentCard: null,
    catalogAgents: [],
    catalogMCPServers: [],
    catalogSkills: [],
    catalogCapabilities: [],
    selectedSkillName: "",
    skillDrawerOpen: false,
    workspace: null,
    governance: null,
    traces: [],
    traceBackend: "broker-waterfall",
    traceAgentURN: "",
    traceExportedSpans: 0,
    traceScrubbedCount: 0,
    selectedTraceID: "",
    evalReport: null,
    architecture: null,
    selectedFlowID: "turn-1-triage",
    selectedNodeID: "sre-gateway",
    activeHopIndex: 0,
    hopTimer: null,
    turnsByIncident: {},
    clearedIncidentChats: {},
    pendingConfirmationByIncident: {},
    busy: false
  };

  function makeSVGIcon(name, sizePx) {
    const sz = sizePx || 18;
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("width", String(sz));
    svg.setAttribute("height", String(sz));
    svg.setAttribute("fill", "currentColor");
    svg.setAttribute("aria-hidden", "true");
    const paths = ICON_PATHS[name] || ICON_PATHS.terminal;
    for (const d of paths) {
      const p = document.createElementNS(SVG_NS, "path");
      p.setAttribute("d", d);
      svg.appendChild(p);
    }
    return svg;
  }

  function makeBrandMarkSVG() {
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("width", "20");
    svg.setAttribute("height", "20");
    svg.setAttribute("aria-hidden", "true");
    const dots = [
      { cx: "7", cy: "7", fill: "var(--cm-brand-blue)" },
      { cx: "17", cy: "7", fill: "var(--cm-brand-red)" },
      { cx: "7", cy: "17", fill: "var(--cm-brand-yellow)" },
      { cx: "17", cy: "17", fill: "var(--cm-brand-green)" }
    ];
    for (const dot of dots) {
      const c = document.createElementNS(SVG_NS, "circle");
      c.setAttribute("cx", dot.cx);
      c.setAttribute("cy", dot.cy);
      c.setAttribute("r", "4.2");
      c.setAttribute("fill", dot.fill);
      svg.appendChild(c);
    }
    return svg;
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  async function apiFetch(path, options, isRetry) {
    const opts = Object.assign({ credentials: "same-origin" }, options || {});
    const headers = Object.assign({}, opts.headers || {});
    if (opts.method === "POST") {
      headers["Content-Type"] = "application/json";
      if (state.csrfToken) {
        headers["X-CSRF-Token"] = state.csrfToken;
      }
    }
    opts.headers = headers;
    const resp = await fetch(path, opts);
    const data = await resp.json().catch(() => ({}));
    if (resp.status === 403 && opts.method === "POST" && !isRetry && path !== "/api/session") {
      try {
        const sessResp = await fetch("/api/session", { credentials: "same-origin" });
        const sessData = await sessResp.json().catch(() => ({}));
        if (sessData && sessData.csrf_token) {
          state.csrfToken = sessData.csrf_token;
          return apiFetch(path, options, true);
        }
      } catch (_) {}
    }
    if (!resp.ok) {
      const err = new Error(data.error || "Request failed with HTTP " + resp.status);
      err.status = resp.status;
      err.payload = data;
      throw err;
    }
    return data;
  }

  function showToast(msg) {
    const box = document.getElementById("status-toast");
    if (!box) {
      return;
    }
    box.textContent = msg;
    box.classList.remove("hidden");
    window.setTimeout(() => {
      box.classList.add("hidden");
    }, 4500);
  }

  function applyTheme(theme, persist) {
    const next = theme === "dark" ? "dark" : "light";
    document.documentElement.setAttribute("data-theme", next);
    const lightBtn = document.getElementById("btn-theme-light");
    const darkBtn = document.getElementById("btn-theme-dark");
    if (lightBtn && darkBtn) {
      lightBtn.classList.toggle("active", next === "light");
      darkBtn.classList.toggle("active", next === "dark");
    }
    if (persist) {
      apiFetch("/api/preferences", {
        method: "POST",
        body: JSON.stringify({ theme: next })
      }).catch(() => {});
    }
  }

  function hydrateStaticIcons() {
    const navToggle = document.getElementById("btn-nav-toggle");
    if (navToggle) {
      navToggle.replaceChildren(makeSVGIcon("menu", 18));
    }
    const brand = document.getElementById("brand-mark");
    if (brand) {
      brand.replaceChildren(makeBrandMarkSVG());
    }
    const cmdkBtn = document.getElementById("btn-cmdk");
    if (cmdkBtn) {
      cmdkBtn.replaceChildren(makeSVGIcon("tune", 18));
    }
    const lightBtn = document.getElementById("btn-theme-light");
    if (lightBtn) {
      lightBtn.replaceChildren(makeSVGIcon("light_mode", 16));
    }
    const darkBtn = document.getElementById("btn-theme-dark");
    if (darkBtn) {
      darkBtn.replaceChildren(makeSVGIcon("dark_mode", 16));
    }
    document.querySelectorAll("[data-icon]").forEach((holder) => {
      const name = holder.getAttribute("data-icon");
      holder.replaceChildren(makeSVGIcon(name, 18));
    });
  }

  function setRoute(pathname, push) {
    const allowed = ["/", "/sessions", "/catalog", "/about"];
    const route = allowed.includes(pathname) ? pathname : "/";
    state.activeRoute = route;
    if (push && window.location.pathname !== route) {
      window.history.pushState({}, "", route);
    }

    document.querySelectorAll(".nav-item").forEach((link) => {
      link.classList.toggle("active", link.getAttribute("data-route") === route);
    });
    const map = {
      "/": "view-investigations",
      "/sessions": "view-sessions",
      "/catalog": "view-catalog",
      "/about": "view-about"
    };
    Object.entries(map).forEach(([r, id]) => {
      const sec = document.getElementById(id);
      if (sec) {
        sec.classList.toggle("hidden", r !== route);
      }
    });

    if (route === "/about") {
      loadArchitecture();
    } else if (route === "/sessions") {
      refreshLedgerAndMemories();
    } else if (route === "/catalog") {
      refreshCatalog();
    }
  }

  function setTab(tabName) {
    state.activeTab = tabName;
    document.querySelectorAll(".tab-btn").forEach((btn) => {
      const isAct = btn.getAttribute("data-tab") === tabName;
      btn.classList.toggle("active", isAct);
      btn.setAttribute("aria-selected", isAct ? "true" : "false");
    });
    ["overview", "governance", "traces"].forEach((t) => {
      const panel = document.getElementById("tab-" + t);
      if (panel) {
        panel.classList.toggle("hidden", t !== tabName);
      }
    });
  }

  function actorNameForIncident(incID) {
    return String(incID || "INC-402").trim().toLowerCase().replace(/_/g, "-");
  }

  async function refreshAll() {
    const memURL = "/api/ledger/memories" + (state.memoryQuery ? "?q=" + encodeURIComponent(state.memoryQuery) : "");
    const [incData, actors, gov, traceDoc, evalDoc, ledgerData, memData, catalogData] = await Promise.all([
      apiFetch("/api/incidents").catch(() => ({ incidents: [], target_status: null })),
      apiFetch("/api/actors").catch(() => []),
      apiFetch("/api/governance").catch(() => ({ events: [] })),
      apiFetch("/api/traces").catch(() => ({ traces: [], backend: "broker-waterfall" })),
      apiFetch("/api/evals").catch(() => null),
      apiFetch("/api/ledger/sessions").catch(() => ({ sessions: [], backend: "broker-session-ledger" })),
      apiFetch(memURL).catch(() => ({ memories: [] })),
      apiFetch("/api/catalog").catch(() => ({ backend: "broker-skill-registry", agent_card: null, agents: [], mcp_servers: [], skills: [], capabilities: [] }))
    ]);
    state.incidents = incData.incidents || [];
    state.targetStatus = incData.target_status || null;
    state.actors = Array.isArray(actors) ? actors : [];
    state.governance = gov;
    if (Array.isArray(traceDoc)) {
      state.traces = traceDoc;
    } else if (traceDoc && typeof traceDoc === "object") {
      state.traces = Array.isArray(traceDoc.traces) ? traceDoc.traces : [];
      state.traceBackend = traceDoc.backend || "broker-waterfall";
      state.traceAgentURN = traceDoc.agent_urn || "";
      state.traceExportedSpans = Number(traceDoc.exported_span_count || 0);
      state.traceScrubbedCount = Number(traceDoc.scrubbed_count || 0);
    } else {
      state.traces = [];
    }
    state.evalReport = evalDoc;
    state.ledgerSessions = Array.isArray(ledgerData.sessions) ? ledgerData.sessions : [];
    state.ledgerBackend = ledgerData.backend || "broker-session-ledger";
    state.memories = Array.isArray(memData.memories) ? memData.memories : [];
    state.catalogBackend = catalogData.backend || "broker-skill-registry";
    state.catalogAgentCard = catalogData.agent_card || null;
    state.catalogAgents = Array.isArray(catalogData.agents) ? catalogData.agents : [];
    state.catalogMCPServers = Array.isArray(catalogData.mcp_servers) ? catalogData.mcp_servers : [];
    state.catalogSkills = Array.isArray(catalogData.skills) ? catalogData.skills : [];
    state.catalogCapabilities = Array.isArray(catalogData.capabilities) ? catalogData.capabilities : [];
    if (!state.selectedSkillName && state.catalogSkills.length > 0) {
      state.selectedSkillName = state.catalogSkills[0].name;
    }

    if (state.incidents.length > 0 && !state.incidents.some((i) => i.id === state.selectedIncidentID)) {
      state.selectedIncidentID = state.incidents[0].id;
    }

    if (!state.selectedLedgerSessionID && state.ledgerSessions.length > 0) {
      state.selectedLedgerSessionID = state.ledgerSessions[0].session_id;
    }
    if (state.selectedLedgerSessionID) {
      state.selectedLedgerDetail = await apiFetch(
        "/api/ledger/sessions/" + encodeURIComponent(state.selectedLedgerSessionID)
      ).catch(() => null);
    }

    const actorName = actorNameForIncident(state.selectedIncidentID);
    state.workspace = await apiFetch("/api/actors/" + encodeURIComponent(actorName) + "/workspace").catch(() => null);

    const existingTurns = state.turnsByIncident[state.selectedIncidentID] || [];
    if (existingTurns.length === 0 && !state.clearedIncidentChats[state.selectedIncidentID]) {
      let incLedgerDetail = null;
      if (state.selectedLedgerDetail && state.selectedLedgerDetail.session_id === actorName) {
        incLedgerDetail = state.selectedLedgerDetail;
      } else if ((state.ledgerSessions || []).some((s) => s.session_id === actorName)) {
        incLedgerDetail = await apiFetch("/api/ledger/sessions/" + encodeURIComponent(actorName)).catch(() => null);
      }
      if (incLedgerDetail && Array.isArray(incLedgerDetail.events) && incLedgerDetail.events.length > 0) {
        const incObj = (state.incidents || []).find((i) => i.id === state.selectedIncidentID);
        const fallbackTmpl = (incObj && incObj.template) || incLedgerDetail.template || "sre-agent-v1";
        const hydrated = hydrateTurnsFromLedgerDetail(state.selectedIncidentID, incLedgerDetail, fallbackTmpl);
        if (hydrated.length > 0) {
          state.turnsByIncident[state.selectedIncidentID] = hydrated;
        }
      }
    }

    renderIncidentSelect();
    renderOverview();
    renderGovernanceTab();
    renderTracesTab();
    renderAssistStream();
    renderSessionsView();
    renderCatalogTable();
  }

  function renderIncidentSelect() {
    const sel = document.getElementById("incident-select");
    if (!sel) {
      return;
    }
    const items = state.incidents.length > 0 ? state.incidents : [{ id: "INC-402", title: "ledger-api 503 spike" }];
    const opts = items.map((inc) => {
      const opt = document.createElement("option");
      opt.value = inc.id;
      opt.textContent = inc.id + " · " + inc.service;
      opt.selected = inc.id === state.selectedIncidentID;
      return opt;
    });
    sel.replaceChildren(...opts);
  }

  function renderOverview() {
    const inc = state.incidents.find((i) => i.id === state.selectedIncidentID) || state.incidents[0];
    const actorName = actorNameForIncident(state.selectedIncidentID);
    const sessPill = document.getElementById("assist-session-pill");
    if (sessPill) {
      sessPill.textContent = "session: " + actorName;
    }

    const actorPill = document.getElementById("actor-state-pill");
    const tmplPill = document.getElementById("actor-template-pill");
    if (inc && actorPill && tmplPill) {
      actorPill.textContent = inc.compute_badge || "On demand";
      actorPill.className = "status-pill " + (
        inc.actor_state === "RUNNING" ? "status-success" :
        inc.actor_state === "SUSPENDED" ? "status-warning" : "status-neutral"
      );
      tmplPill.textContent = inc.template || "sre-agent-v1";
    }

    const serverGate = inc && inc.pending_gate ? inc.pending_gate : null;
    const pendingCallID = (serverGate && serverGate.call_id) || state.pendingConfirmationByIncident[state.selectedIncidentID] || "";
    const oauth3lo = (state.governance && state.governance.oauth_3lo) || {};
    const need3LOConsent = Boolean(oauth3lo.required_for_remediation && !oauth3lo.granted);
    const gateBanner = document.getElementById("gate-banner");
    if (gateBanner) {
      gateBanner.classList.toggle("hidden", !pendingCallID);
      const gateDetail = document.getElementById("gate-banner-detail");
      const gateActorPill = document.getElementById("gate-actor-pill");
      const gate3LOPill = document.getElementById("gate-3lo-pill");
      const auth3LOBtn = document.getElementById("btn-authorize-3lo");
      const approveBtn = document.getElementById("btn-approve-gate");
      if (gateActorPill) {
        const isSuspended = !inc || inc.actor_state === "SUSPENDED" || Boolean(serverGate && serverGate.actor_suspended);
        gateActorPill.textContent = isSuspended ? "Suspended · 0 vCPU" : "Running · awaiting decision";
        gateActorPill.className = "status-pill " + (isSuspended ? "status-warning" : "status-neutral");
      }
      if (gate3LOPill) {
        gate3LOPill.classList.toggle("hidden", !oauth3lo.required_for_remediation);
        if (oauth3lo.granted) {
          gate3LOPill.textContent = "3LO granted · " + (oauth3lo.operator_sub || "sre-operator");
          gate3LOPill.className = "status-pill status-success";
        } else {
          gate3LOPill.textContent = "3LO consent required";
          gate3LOPill.className = "status-pill status-warning";
        }
      }
      if (auth3LOBtn && approveBtn) {
        auth3LOBtn.classList.toggle("hidden", !need3LOConsent);
        approveBtn.classList.toggle("hidden", need3LOConsent);
      }
      if (gateDetail && serverGate) {
        const branchLabel = serverGate.branch || (serverGate.args && serverGate.args.branch) || "fix/inc-402-pool-leak";
        const shaLabel = serverGate.commit_sha ? " (" + serverGate.commit_sha + ")" : "";
        const toolLabel = serverGate.tool_name || "apply_remediation_pr";
        const stepSuffix = need3LOConsent
          ? "Authorize 3LO remediation first to mint a short-lived server-side token before waking " + (serverGate.actor_name || actorName) + "."
          : "Approve to wake " + (serverGate.actor_name || actorName) + " (<400ms) and execute " + toolLabel + ", or reject to abort merge.";
        gateDetail.textContent =
          "Verified branch " + branchLabel + shaLabel + " is staged in /workspace/ledger-api. " + stepSuffix;
      } else if (gateDetail) {
        gateDetail.textContent = "Review the staged /workspace patch before approving production merge.";
      }
    }

    if (inc) {
      document.getElementById("inc-card-title").textContent = inc.id + " · " + inc.title;
      const sevPill = document.getElementById("inc-severity-pill");
      sevPill.textContent = inc.severity + " · " + inc.status;
      sevPill.className = "status-pill " + (inc.status === "RESOLVED" ? "status-success" : "status-error");
      document.getElementById("inc-summary-text").textContent = inc.summary || "";

      const symList = document.getElementById("inc-symptoms-list");
      symList.replaceChildren(...(inc.symptoms || []).map((s) => el("li", "", s)));

      const noteBox = document.getElementById("inc-note-box");
      const notePre = document.getElementById("inc-note-pre");
      if (inc.customer_note) {
        noteBox.classList.remove("hidden");
        notePre.textContent = inc.customer_note;
      } else {
        noteBox.classList.add("hidden");
        notePre.textContent = "";
      }
    }

    const sloPill = document.getElementById("ledger-slo-pill");
    const metricsGrid = document.getElementById("ledger-metrics-grid");
    if (state.targetStatus) {
      const ts = state.targetStatus;
      sloPill.textContent = ts.slo_met ? "Healthy · SLO met" : "Degraded · pool exhausted";
      sloPill.className = "status-pill " + (ts.slo_met ? "status-success" : "status-error");
      metricsGrid.replaceChildren(
        makeKV("Status", ts.status),
        makeKV("Max open conns", String(ts.max_open_conns)),
        makeKV("Active leaked workers", String(ts.active_leaked_workers)),
        makeKV("p99 latency", ts.p99_latency_ms + " ms")
      );
    } else {
      sloPill.textContent = "Offline / local";
      sloPill.className = "status-pill status-neutral";
      metricsGrid.replaceChildren(makeKV("Target service", "ledger-api in prod-sim"));
    }

    const ws = state.workspace;
    const wsPill = document.getElementById("ws-verify-pill");
    const wsMeta = document.getElementById("ws-meta-grid");
    const wsFindings = document.getElementById("ws-findings-list");
    const wsDiff = document.getElementById("ws-diff-pre");

    if (ws && ws.cloned) {
      wsPill.textContent = ws.verify_ok ? "Verified · ledger-verify OK" : "Cloned · unpatched findings";
      wsPill.className = "status-pill " + (ws.verify_ok ? "status-success" : "status-warning");
      wsMeta.replaceChildren(
        makeKV("Workspace path", "/workspace/" + (ws.repo_dir || "ledger-api")),
        makeKV("Git branch", ws.branch || "main"),
        makeKV("HEAD commit", (ws.head_sha || "-") + (ws.head_subject ? " · " + ws.head_subject : "")),
        makeKV("Git status", ws.status_short || "clean")
      );
      wsFindings.replaceChildren(...(ws.verify_findings || []).map((f) => el("li", "mono", f)));
      wsDiff.textContent = ws.diff || "(Working tree matches HEAD on branch " + (ws.branch || "main") + ")";
    } else {
      wsPill.textContent = "Not cloned yet";
      wsPill.className = "status-pill status-neutral";
      wsMeta.replaceChildren(
        makeKV("Workspace mount", "/workspace (Substrate durableDir)"),
        makeKV("Checkout status", "Awaiting Turn 1 clone_repo")
      );
      wsFindings.replaceChildren();
      wsDiff.textContent = "Send a triage prompt in the SRE triage agent pane to wake the Incident Actor and clone ledger-api.git.";
    }
  }

  function makeKV(term, val) {
    const wrap = el("div", "kv-item");
    const dt = el("dt", "kv-term", term);
    const dd = el("dd", "kv-val mono", val);
    wrap.append(dt, dd);
    return wrap;
  }

  function verdictBadgeClass(verdict) {
    const v = String(verdict || "").toUpperCase();
    if (v === "ALLOW" || v === "READY" || v === "GRANTED" || v === "APPROVE") {
      return "status-success";
    }
    if (v === "SUSPENDED" || v === "REDACTED" || v === "CONSENT_REQUIRED" || v === "REVOKED" || v === "REJECT") {
      return "status-warning";
    }
    if (v === "MODEL_ARMOR_BLOCKED" || v === "DENY" || v.startsWith("DENIED_") || v === "ERROR" || v.startsWith("HTTP_")) {
      return "status-error";
    }
    return "status-neutral";
  }

  function renderGovernanceTab() {
    const gov = state.governance || { events: [] };
    const pill = document.getElementById("gov-egress-pill");
    if (pill && gov.egress_host) {
      pill.textContent = "egress -> " + gov.egress_host;
    }

    const idInfo = gov.identity || {};
    const topSpiffePill = document.getElementById("spiffe-pill");
    if (topSpiffePill && idInfo.mode) {
      topSpiffePill.textContent = "SPIFFE · " + idInfo.mode + (idInfo.svid_mounted ? " · X.509 SVID" : "");
      topSpiffePill.className = "status-pill " + (idInfo.svid_mounted ? "status-success" : "status-neutral");
    }

    const idPill = document.getElementById("gov-identity-pill");
    const idGrid = document.getElementById("gov-identity-grid");
    if (idPill && idGrid) {
      idPill.textContent = idInfo.svid_mounted
        ? "X.509 SVID mounted (" + (idInfo.mode || "org") + ")"
        : "Workload Identity (" + (idInfo.mode || "wif") + ")";
      idPill.className = "status-pill " + (idInfo.svid_mounted ? "status-success" : "status-neutral");
      idGrid.replaceChildren(
        makeKV("Trust domain", idInfo.trust_domain || gov.spiffe_domain || "-"),
        makeKV("Broker principal", idInfo.principal || "-"),
        makeKV("Agent SVID expiry", idInfo.svid_expires_at || "Metadata server token"),
        makeKV("atenet mTLS cert", idInfo.atenet_cert_mounted ? (idInfo.atenet_spiffe_id || "Mounted") : "Unmounted (local dev)")
      );
    }

    const maInfo = gov.model_armor || {};
    const mcpInfo = gov.mcp_policy || {};
    const armorPill = document.getElementById("gov-armor-pill");
    const armorGrid = document.getElementById("gov-armor-grid");
    if (armorPill && armorGrid) {
      armorPill.textContent = maInfo.template_id ? "Template · " + maInfo.template_id : "Defense-in-depth active";
      armorPill.className = "status-pill " + ((maInfo.blocked_count || 0) > 0 ? "status-error" : "status-success");
      armorGrid.replaceChildren(
        makeKV("Screening mode", maInfo.mode || "defense-in-depth"),
        makeKV("Blocked injections", String(maInfo.blocked_count || 0)),
        makeKV("SDP PII redactions", String(maInfo.redacted_count || 0)),
        makeKV("MCP 403 denials", String(mcpInfo.denied_count || 0) + " denied / " + String(mcpInfo.allowed_count || 0) + " allowed")
      );
    }

    const oauth = gov.oauth_3lo || {};
    const pulls = Array.isArray(gov.recent_pulls) ? gov.recent_pulls : [];
    const latestPR = pulls.length > 0 ? pulls[pulls.length - 1] : null;
    const attributionText = latestPR && latestPR.operator_sub
      ? latestPR.operator_sub + (latestPR.agent_identity ? " + " + latestPR.agent_identity : "")
      : (oauth.granted ? (oauth.operator_sub || "sre-operator") + " (ready)" : "Awaiting 3LO remediation");

    const oauthPill = document.getElementById("gov-oauth-pill");
    const oauthGrid = document.getElementById("gov-oauth-grid");
    const govAuthBtn = document.getElementById("btn-gov-authorize-3lo");
    const govRevBtn = document.getElementById("btn-gov-revoke-3lo");
    if (oauthPill && oauthGrid) {
      if (oauth.granted) {
        oauthPill.textContent = "3LO granted · " + (oauth.ttl_remaining_seconds || 0) + "s TTL";
        oauthPill.className = "status-pill status-success";
      } else if (oauth.required_for_remediation) {
        oauthPill.textContent = "3LO required for remediation";
        oauthPill.className = "status-pill status-warning";
      } else {
        oauthPill.textContent = "0 tokens in sandbox";
        oauthPill.className = "status-pill status-neutral";
      }
      oauthGrid.replaceChildren(
        makeKV("3LO provider", oauth.auth_provider_id || oauth.provider || "ops-hub-3lo"),
        makeKV("Vault storage", oauth.granted ? "In-memory only (" + (oauth.auth_method || "pkce") + ")" : "Server-side in-memory (empty)"),
        makeKV("Sandbox & GCS tokens", "0 bearer tokens across Actor Boundary"),
        makeKV("Dual PR attribution", attributionText)
      );
    }
    if (govAuthBtn) {
      govAuthBtn.textContent = oauth.granted ? "Refresh 3LO grant" : "Authorize 3LO remediation";
    }
    if (govRevBtn) {
      govRevBtn.classList.toggle("hidden", !oauth.granted);
    }

    renderCmdKOAuthRow(oauth);

    const tbody = document.getElementById("gov-table-body");
    const rows = (gov.events || []).map((ev) => {
      const tr = document.createElement("tr");
      const verdictTd = document.createElement("td");
      verdictTd.appendChild(el("span", "status-pill mono " + verdictBadgeClass(ev.verdict), ev.verdict || "-"));
      tr.append(
        el("td", "mono", ev.timestamp || ""),
        el("td", "", ev.boundary || ""),
        el("td", "mono", ev.action || ""),
        verdictTd,
        el("td", "", ev.detail || "")
      );
      return tr;
    });
    tbody.replaceChildren(...rows);
  }

  function renderCmdKOAuthRow(oauth) {
    const cmdkPill = document.getElementById("cmdk-oauth-pill");
    const cmdkAuthBtn = document.getElementById("btn-cmdk-authorize-3lo");
    const cmdkRevBtn = document.getElementById("btn-cmdk-revoke-3lo");
    if (cmdkPill) {
      if (oauth && oauth.granted) {
        cmdkPill.textContent = "Granted · " + (oauth.operator_sub || "sre-operator") + " (" + (oauth.ttl_remaining_seconds || 0) + "s)";
        cmdkPill.className = "status-pill status-success";
      } else {
        cmdkPill.textContent = oauth && oauth.required_for_remediation ? "Consent required" : "Not granted";
        cmdkPill.className = "status-pill " + (oauth && oauth.required_for_remediation ? "status-warning" : "status-neutral");
      }
    }
    if (cmdkAuthBtn) {
      cmdkAuthBtn.textContent = oauth && oauth.granted ? "Refresh 3LO grant" : "Authorize 3LO remediation";
    }
    if (cmdkRevBtn) {
      cmdkRevBtn.classList.toggle("hidden", !(oauth && oauth.granted));
    }
  }

  async function start3LOConsent() {
    try {
      const res = await apiFetch("/api/oauth/start", {
        method: "POST",
        body: JSON.stringify({})
      });
      if (res.granted) {
        showToast("Short-lived 3LO grant active for " + (res.operator_sub || "sre-operator") + ".");
        await refreshAll();
        return;
      }
      if (res.auth_url) {
        const target = new URL(res.auth_url, window.location.origin);
        if (target.protocol !== "http:" && target.protocol !== "https:") {
          throw new Error("Rejected non-HTTP OAuth redirect protocol");
        }
        window.location.assign(target.toString());
      }
    } catch (err) {
      showToast("3LO authorization start failed: " + err.message);
    }
  }

  async function revoke3LOGrant() {
    try {
      await apiFetch("/api/oauth/revoke", {
        method: "POST",
        body: JSON.stringify({})
      });
      showToast("Revoked short-lived 3LO grant from sre-gateway memory.");
      await refreshAll();
    } catch (err) {
      showToast("Revoke failed: " + err.message);
    }
  }

  function spanBarModifier(span) {
    if (!span) {
      return "";
    }
    if (String(span.status || "").toUpperCase() === "ERROR") {
      return " bar-error";
    }
    const boundary = String(span.boundary || "").toLowerCase();
    if (boundary.includes("incident actor") || span.service === "sre-agent") {
      return " bar-actor";
    }
    if (boundary.includes("remediation gate") || boundary.includes("lifecycle")) {
      return " bar-gate";
    }
    return "";
  }

  function renderTracesTab() {
    const backendPill = document.getElementById("trace-backend-pill");
    const scrubPill = document.getElementById("trace-scrub-pill");
    const traceSelect = document.getElementById("trace-select");
    const traceMeta = document.getElementById("trace-meta-grid");
    const box = document.getElementById("trace-waterfall-box");

    if (backendPill) {
      backendPill.textContent = state.traceBackend === "cloud-trace"
        ? "Cloud Trace · W3C traceparent"
        : "Governed Broker waterfall · W3C traceparent";
      backendPill.className = "status-pill " + (state.traceBackend === "cloud-trace" ? "status-success" : "status-neutral");
    }
    if (scrubPill) {
      scrubPill.textContent = String(state.traceScrubbedCount || 0) + " attributes scrubbed · 0 token leaks";
    }

    const traces = state.traces || [];
    if (traces.length > 0) {
      const stillExists = traces.some((t) => t.trace_id === state.selectedTraceID);
      if (!stillExists) {
        const matchInc = traces.find((t) => t.incident_id === state.selectedIncidentID);
        state.selectedTraceID = (matchInc || traces[0]).trace_id;
      }
    } else {
      state.selectedTraceID = "";
    }

    if (traceSelect) {
      if (traces.length === 0) {
        const opt = document.createElement("option");
        opt.value = "";
        opt.textContent = "No traces yet";
        traceSelect.replaceChildren(opt);
      } else {
        const opts = traces.map((tr) => {
          const opt = document.createElement("option");
          opt.value = tr.trace_id;
          const shortID = String(tr.trace_id || "").slice(0, 12);
          opt.textContent = (tr.incident_id || "TURN") + " · " + shortID + " · " + Number(tr.duration_ms || 0).toFixed(1) + " ms (" + (tr.span_count || (tr.spans || []).length) + " spans)";
          opt.selected = tr.trace_id === state.selectedTraceID;
          return opt;
        });
        traceSelect.replaceChildren(...opts);
      }
    }

    const activeTrace = traces.find((t) => t.trace_id === state.selectedTraceID) || traces[0] || null;
    if (traceMeta) {
      if (!activeTrace) {
        traceMeta.replaceChildren(
          makeKV("Trace context", "W3C traceparent (00-traceId-spanId-01)"),
          makeKV("Agent Registry URN", state.traceAgentURN || "projects/-/locations/-/agents/platform-sre-triage-agent"),
          makeKV("OTLP ingest route", "POST /otlp/v1/traces (keyless across EgressPolicy)"),
          makeKV("Secret & PII scrubbing", "Active (Authorization, Cookie, tokens, raw tool payloads stripped)")
        );
      } else {
        traceMeta.replaceChildren(
          makeKV("Trace ID", activeTrace.trace_id || "-"),
          makeKV("Session & template", (activeTrace.session_id || "-") + " · " + (activeTrace.template || "-")),
          makeKV("Agent Registry URN", activeTrace.agent_urn || state.traceAgentURN || "-"),
          makeKV("Gemini token usage", String(activeTrace.input_tokens || 0) + " in / " + String(activeTrace.output_tokens || 0) + " out")
        );
      }
    }

    if (box) {
      if (!activeTrace || !Array.isArray(activeTrace.spans) || activeTrace.spans.length === 0) {
        box.replaceChildren(
          el("p", "secondary-copy", "No OpenTelemetry GenAI waterfall spans recorded yet. Dispatch a turn in the SRE triage agent pane to propagate W3C traceparent across the Actor Boundary.")
        );
      } else {
        const rows = activeTrace.spans.map((sp) => {
          const det = document.createElement("details");
          det.className = "waterfall-row";

          const sum = document.createElement("summary");
          sum.className = "waterfall-row-summary";

          const main = el("div", "waterfall-row-main");
          const leftMeta = el("div", "waterfall-span-meta");
          const depthPx = Math.min(4, Math.max(0, Number(sp.depth || 0))) * 12;
          leftMeta.style.paddingLeft = depthPx + "px";

          const nameSpan = el("span", "waterfall-span-name mono", sp.name || "span");
          const boundaryPill = el("span", "stroked-pill", sp.boundary || sp.service || "Broker");
          leftMeta.append(nameSpan, boundaryPill);

          const track = el("div", "waterfall-track");
          const bar = el("div", "waterfall-bar" + spanBarModifier(sp));
          const leftPct = Math.max(0, Math.min(96, Number(sp.offset_pct || 0)));
          const widthPct = Math.max(2, Math.min(100 - leftPct, Number(sp.width_pct || 2)));
          bar.style.left = leftPct.toFixed(2) + "%";
          bar.style.width = widthPct.toFixed(2) + "%";
          track.appendChild(bar);

          const durCol = el("span", "waterfall-duration mono", Number(sp.duration_ms || 0).toFixed(1) + " ms");
          main.append(leftMeta, track, durCol);
          sum.appendChild(main);

          const attrLines = [
            "span_id: " + (sp.span_id || "-") + "  parent_span_id: " + (sp.parent_span_id || "(root)") + "  kind: " + (sp.kind || "INTERNAL") + "  status: " + (sp.status || "OK")
          ];
          if (sp.status_message) {
            attrLines.push("status_message: " + sp.status_message);
          }
          const attrs = sp.attributes || {};
          for (const k of Object.keys(attrs).sort()) {
            attrLines.push(k + ": " + String(attrs[k]));
          }
          const pre = el("pre", "code-block", attrLines.join("\n"));
          det.append(sum, pre);
          return det;
        });
        box.replaceChildren(...rows);
      }
    }

    renderOnlineEvalsSection();
  }

  function renderOnlineEvalsSection() {
    const summaryPill = document.getElementById("eval-summary-pill");
    const metaCopy = document.getElementById("eval-meta-copy");
    const grid = document.getElementById("eval-rubrics-grid");
    if (!grid) {
      return;
    }

    const rep = state.evalReport;
    if (!rep || !Array.isArray(rep.rubrics) || rep.rubrics.length === 0) {
      if (summaryPill) {
        summaryPill.textContent = "Awaiting evaluation";
        summaryPill.className = "status-pill status-neutral";
      }
      grid.replaceChildren(
        el("p", "secondary-copy", "Click Run online eval (or run make eval) to score all 4 trajectory, skill, safety, and memory rubrics.")
      );
      return;
    }

    if (summaryPill) {
      const scoreText = Number(rep.overall_score || 0).toFixed(2);
      if (!rep.has_run) {
        summaryPill.textContent = "Live preview · " + scoreText;
        summaryPill.className = "status-pill status-neutral";
      } else if (rep.passed) {
        summaryPill.textContent = "Passed · " + scoreText;
        summaryPill.className = "status-pill status-success";
      } else {
        summaryPill.textContent = "Below threshold · " + scoreText;
        summaryPill.className = "status-pill status-warning";
      }
    }

    if (metaCopy) {
      metaCopy.textContent = "Judge: " + (rep.judge_backend || "deterministic") +
        " (" + (rep.model || "gemini-3.8-flash") + ") · threshold " +
        Number(rep.threshold || 0.8).toFixed(2) + " · trajectory digest " + (rep.trajectory_digest || "-");
    }

    const cards = rep.rubrics.map((rub) => {
      const card = el("article", "eval-rubric-card");
      const hdr = el("div", "card-header");
      const title = el("h3", "card-title", rub.title || rub.id);
      const scoreVal = Number(rub.score || 0);
      const pillCls = rub.passed ? "status-success" : "status-warning";
      const pillLabel = (rub.passed ? "Pass · " : "Pending · ") + scoreVal.toFixed(2);
      const pill = el("span", "status-pill mono " + pillCls, pillLabel);
      hdr.append(title, pill);

      const barTrack = el("div", "eval-score-bar-track");
      const barFill = el("div", "eval-score-bar-fill " + (rub.passed ? "score-pass" : "score-fail"));
      barFill.style.width = Math.max(2, Math.min(100, Math.round(scoreVal * 100))) + "%";
      barTrack.appendChild(barFill);

      const idLine = el("span", "mono secondary-copy", rub.id + " (threshold " + Number(rub.threshold || 0.8).toFixed(2) + ")");
      const rationale = el("p", "body-copy", rub.rationale || rub.description || "");
      const evList = el("ul", "symptom-list");
      for (const item of rub.evidence || []) {
        evList.appendChild(el("li", "secondary-copy", item));
      }

      card.append(hdr, barTrack, idLine, rationale, evList);
      return card;
    });
    grid.replaceChildren(...cards);
  }

  async function runOnlineEvals() {
    const btn = document.getElementById("btn-run-evals");
    if (btn) {
      btn.disabled = true;
    }
    try {
      const rep = await apiFetch("/api/evals/run", {
        method: "POST",
        body: JSON.stringify({})
      });
      state.evalReport = rep;
      renderOnlineEvalsSection();
      showToast(rep.passed
        ? "Online evaluation passed (overall score " + Number(rep.overall_score || 0).toFixed(2) + ")."
        : "Online evaluation completed (overall score " + Number(rep.overall_score || 0).toFixed(2) + " below threshold).");
      await refreshAll();
    } catch (err) {
      showToast("Online evaluation failed: " + err.message);
    } finally {
      if (btn) {
        btn.disabled = false;
      }
    }
  }

  function makeToolStepElement(st) {
    const det = document.createElement("details");
    det.className = "tool-step-details";
    const sum = document.createElement("summary");
    sum.className = "tool-step-summary";
    sum.textContent = st.name + (st.duration_ms ? " (" + st.duration_ms + " ms)" : "");
    const pre = el("pre", "code-block", "args: " + (st.args || "{}") + "\n\nresult: " + (st.result || ""));
    det.append(sum, pre);
    return det;
  }

  function hydrateTurnsFromLedgerDetail(incID, detail, fallbackTemplate) {
    const events = Array.isArray(detail && detail.events) ? detail.events : [];
    const out = [];
    let pendingTools = [];
    const callById = {};

    for (const ev of events) {
      if (!ev) {
        continue;
      }
      const isUser = ev.kind === "prompt" || String(ev.author || "").toLowerCase() === "user";
      if (isUser && ev.text) {
        out.push({
          role: "user",
          text: ev.text,
          metaRight: incID
        });
        continue;
      }

      for (const tc of ev.tool_calls || []) {
        if (!tc || !tc.name) {
          continue;
        }
        const stepObj = {
          name: tc.name,
          duration_ms: 0,
          args: tc.payload || "{}",
          result: ""
        };
        pendingTools.push(stepObj);
        if (tc.id) {
          callById[tc.id] = stepObj;
        }
      }

      for (const tr of ev.tool_responses || []) {
        if (!tr || !tr.name) {
          continue;
        }
        const matched = (tr.id && callById[tr.id]) ||
          pendingTools.find((s) => s.name === tr.name && !s.result);
        if (matched) {
          matched.result = tr.payload || "";
        } else {
          pendingTools.push({
            name: tr.name,
            duration_ms: 0,
            args: "{}",
            result: tr.payload || ""
          });
        }
      }

      if (!isUser && ev.text && String(ev.text).trim() !== "") {
        out.push({
          role: "model",
          text: ev.text,
          template: fallbackTemplate || detail.template || "sre-agent-v1",
          metaRight: ev.timestamp || "Session Ledger",
          toolSteps: pendingTools
        });
        pendingTools = [];
      }
    }

    if (pendingTools.length > 0 && out.length > 0) {
      const last = out[out.length - 1];
      if (last.role === "model") {
        last.toolSteps = (last.toolSteps || []).concat(pendingTools);
      } else {
        out.push({
          role: "model",
          text: "Executed " + pendingTools.length + " tool step(s) in session " + (detail.session_id || incID) + ".",
          template: fallbackTemplate || detail.template || "sre-agent-v1",
          metaRight: "Session Ledger",
          toolSteps: pendingTools
        });
      }
    }
    return out;
  }

  function appendCodeTokens(parent, rawText) {
    const str = String(rawText || "");
    const codeRe = /`([^`]+)`/g;
    let lastIdx = 0;
    let m;
    while ((m = codeRe.exec(str)) !== null) {
      if (m.index > lastIdx) {
        parent.appendChild(document.createTextNode(str.slice(lastIdx, m.index)));
      }
      parent.appendChild(el("code", "md-inline-code", m[1]));
      lastIdx = codeRe.lastIndex;
    }
    if (lastIdx < str.length) {
      parent.appendChild(document.createTextNode(str.slice(lastIdx)));
    }
  }

  function appendInlineMarkdown(parent, rawText) {
    const str = String(rawText || "");
    const tokenRe = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*\n]+\*)/g;
    let lastIndex = 0;
    let match;
    while ((match = tokenRe.exec(str)) !== null) {
      if (match.index > lastIndex) {
        parent.appendChild(document.createTextNode(str.slice(lastIndex, match.index)));
      }
      const token = match[0];
      if (token.startsWith("`") && token.endsWith("`")) {
        parent.appendChild(el("code", "md-inline-code", token.slice(1, -1)));
      } else if (token.startsWith("**") && token.endsWith("**")) {
        const strongEl = document.createElement("strong");
        appendCodeTokens(strongEl, token.slice(2, -2));
        parent.appendChild(strongEl);
      } else if (token.startsWith("*") && token.endsWith("*")) {
        const emEl = document.createElement("em");
        appendCodeTokens(emEl, token.slice(1, -1));
        parent.appendChild(emEl);
      } else {
        parent.appendChild(document.createTextNode(token));
      }
      lastIndex = tokenRe.lastIndex;
    }
    if (lastIndex < str.length) {
      parent.appendChild(document.createTextNode(str.slice(lastIndex)));
    }
  }

  function renderMarkdownBlock(rawText) {
    const root = el("div", "turn-markdown");
    const lines = String(rawText || "").replace(/\r\n/g, "\n").split("\n");
    let inCodeFence = false;
    let codeLines = [];
    let currentList = null;
    let currentListTag = "";
    let paraLines = [];

    function flushParagraph() {
      if (paraLines.length === 0) {
        return;
      }
      const p = document.createElement("p");
      appendInlineMarkdown(p, paraLines.join(" "));
      root.appendChild(p);
      paraLines = [];
    }

    function flushList() {
      if (currentList) {
        root.appendChild(currentList);
        currentList = null;
        currentListTag = "";
      }
    }

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed.startsWith("```")) {
        flushParagraph();
        flushList();
        if (inCodeFence) {
          root.appendChild(el("pre", "code-block", codeLines.join("\n")));
          codeLines = [];
          inCodeFence = false;
        } else {
          inCodeFence = true;
        }
        continue;
      }
      if (inCodeFence) {
        codeLines.push(line);
        continue;
      }
      if (trimmed === "") {
        flushParagraph();
        flushList();
        continue;
      }

      const headingMatch = /^(#{1,4})\s+(.*)$/.exec(trimmed);
      if (headingMatch) {
        flushParagraph();
        flushList();
        const hTag = headingMatch[1].length <= 2 ? "h3" : "h4";
        const hEl = document.createElement(hTag);
        appendInlineMarkdown(hEl, headingMatch[2]);
        root.appendChild(hEl);
        continue;
      }

      const ulMatch = /^[-*•]\s+(.*)$/.exec(trimmed);
      if (ulMatch) {
        flushParagraph();
        if (currentListTag !== "ul") {
          flushList();
          currentList = document.createElement("ul");
          currentListTag = "ul";
        }
        const li = document.createElement("li");
        appendInlineMarkdown(li, ulMatch[1]);
        currentList.appendChild(li);
        continue;
      }

      const olMatch = /^\d+\.\s+(.*)$/.exec(trimmed);
      if (olMatch) {
        flushParagraph();
        if (currentListTag !== "ol") {
          flushList();
          currentList = document.createElement("ol");
          currentListTag = "ol";
        }
        const li = document.createElement("li");
        appendInlineMarkdown(li, olMatch[1]);
        currentList.appendChild(li);
        continue;
      }

      flushList();
      paraLines.push(trimmed);
    }

    if (inCodeFence && codeLines.length > 0) {
      root.appendChild(el("pre", "code-block", codeLines.join("\n")));
    }
    flushParagraph();
    flushList();
    return root;
  }

  async function applyPresetPrompt(preset) {
    if (preset.incident && preset.incident !== state.selectedIncidentID) {
      state.selectedIncidentID = preset.incident;
      const selectEl = document.getElementById("incident-select");
      if (selectEl) {
        selectEl.value = preset.incident;
      }
      await refreshAll();
    }
    const input = document.getElementById("assist-input");
    if (input) {
      input.value = preset.prompt;
      input.focus();
    }
  }

  function renderAssistPresets() {
    const holder = document.getElementById("assist-presets");
    if (!holder) {
      return;
    }
    const chips = PROMPT_PRESETS.map((preset) => {
      const btn = el("button", "preset-pill-btn", preset.label);
      btn.type = "button";
      btn.title = preset.prompt;
      btn.addEventListener("click", () => {
        applyPresetPrompt(preset);
      });
      return btn;
    });
    holder.replaceChildren(...chips);
  }

  function renderAssistStream() {
    renderAssistPresets();
    const stream = document.getElementById("assist-stream");
    const turns = state.turnsByIncident[state.selectedIncidentID] || [];
    if (turns.length === 0) {
      const box = el("div", "starter-box");
      box.appendChild(el("p", "secondary-copy", "Select a starter investigation prompt to populate the composer, then click Send to wake Incident Actor " + actorNameForIncident(state.selectedIncidentID) + ":"));
      for (const preset of PROMPT_PRESETS) {
        const chip = el("button", "starter-chip", preset.prompt);
        chip.type = "button";
        chip.addEventListener("click", () => {
          applyPresetPrompt(preset);
        });
        box.appendChild(chip);
      }
      stream.replaceChildren(box);
      return;
    }

    const nodes = turns.map((turn) => {
      const card = el("div", "turn-card " + (turn.role === "user" ? "turn-user" : "turn-model"));
      const meta = el("div", "turn-meta");
      meta.append(
        el("span", "", turn.role === "user" ? "Operator" : "SRE triage agent (" + (turn.template || "sre-agent-v1") + ")"),
        el("span", "mono", turn.metaRight || "")
      );
      const bodyNode = turn.role === "user"
        ? el("p", "turn-text", turn.text)
        : renderMarkdownBlock(turn.text);
      card.append(meta, bodyNode);
      for (const st of turn.toolSteps || []) {
        card.appendChild(makeToolStepElement(st));
      }
      return card;
    });
    stream.replaceChildren(...nodes);
    stream.scrollTop = stream.scrollHeight;
  }

  async function submitChatTurn(opts) {
    if (state.busy) {
      return;
    }
    const incID = state.selectedIncidentID;
    const input = document.getElementById("assist-input");
    const sendBtn = document.getElementById("btn-send-chat");
    const promptVal = opts && opts.prompt !== undefined ? opts.prompt : input.value.trim();

    if (!opts?.confirmationCallID && !promptVal) {
      return;
    }

    state.busy = true;
    sendBtn.disabled = true;
    sendBtn.textContent = "Running...";

    if (!state.turnsByIncident[incID]) {
      state.turnsByIncident[incID] = [];
    }
    delete state.clearedIncidentChats[incID];
    if (opts?.confirmationCallID) {
      state.turnsByIncident[incID].push({
        role: "user",
        text: opts.confirmed ? "Approved Remediation Gate confirmation." : "Rejected Remediation Gate confirmation.",
        metaRight: incID
      });
    } else {
      state.turnsByIncident[incID].push({
        role: "user",
        text: promptVal,
        metaRight: incID
      });
      input.value = "";
    }
    renderAssistStream();

    try {
      const payload = {
        incident_id: incID,
        prompt: promptVal
      };
      if (opts?.confirmationCallID) {
        payload.confirmation_call_id = opts.confirmationCallID;
        payload.confirmation_confirmed = Boolean(opts.confirmed);
      }
      const res = await apiFetch("/api/chat", {
        method: "POST",
        body: JSON.stringify(payload)
      });
      if (res.remediation_gate_pending && res.confirmation_call_id) {
        state.pendingConfirmationByIncident[incID] = res.confirmation_call_id;
      } else {
        delete state.pendingConfirmationByIncident[incID];
      }
      state.turnsByIncident[incID].push({
        role: "model",
        text: res.reply || "(Empty response)",
        template: res.template,
        metaRight: (res.duration_ms || 0) + " ms",
        toolSteps: res.tool_steps || []
      });
      await refreshAll();
    } catch (err) {
      if (err && err.payload && err.payload.verdict === "MODEL_ARMOR_BLOCKED") {
        const tmplLabel = err.payload.template_id || "defense-in-depth";
        state.turnsByIncident[incID].push({
          role: "model",
          text: "Blocked by Model Armor (" + tmplLabel + "): " + (err.payload.reason || err.message) +
            "\n\nZero untrusted instructions or destructive tool calls crossed into the Incident Actor sandbox.",
          template: "Model Armor",
          metaRight: "HTTP 403 · BLOCKED"
        });
        showToast("Blocked by Model Armor (" + tmplLabel + ") before reaching Incident Actor.");
        await refreshAll();
      } else {
        state.turnsByIncident[incID].push({
          role: "model",
          text: "Turn error: " + err.message,
          template: "sre-gateway",
          metaRight: "error"
        });
        renderAssistStream();
      }
    } finally {
      state.busy = false;
      sendBtn.disabled = false;
      sendBtn.textContent = "Send";
    }
  }

  async function refreshLedgerAndMemories() {
    const memURL = "/api/ledger/memories" + (state.memoryQuery ? "?q=" + encodeURIComponent(state.memoryQuery) : "");
    const [actors, ledgerData, memData] = await Promise.all([
      apiFetch("/api/actors").catch(() => []),
      apiFetch("/api/ledger/sessions").catch(() => ({ sessions: [], backend: "broker-session-ledger" })),
      apiFetch(memURL).catch(() => ({ memories: [] }))
    ]);
    state.actors = Array.isArray(actors) ? actors : [];
    state.ledgerSessions = Array.isArray(ledgerData.sessions) ? ledgerData.sessions : [];
    state.ledgerBackend = ledgerData.backend || "broker-session-ledger";
    state.memories = Array.isArray(memData.memories) ? memData.memories : [];

    if (!state.selectedLedgerSessionID && state.ledgerSessions.length > 0) {
      state.selectedLedgerSessionID = state.ledgerSessions[0].session_id;
    }
    if (state.selectedLedgerSessionID) {
      state.selectedLedgerDetail = await apiFetch(
        "/api/ledger/sessions/" + encodeURIComponent(state.selectedLedgerSessionID)
      ).catch(() => null);
    }
    renderSessionsView();
  }

  async function inspectLedgerSession(sessionID) {
    state.selectedLedgerSessionID = sessionID;
    state.selectedLedgerDetail = await apiFetch(
      "/api/ledger/sessions/" + encodeURIComponent(sessionID)
    ).catch(() => null);
    renderSessionsView();
  }

  function makeComputeBadgePill(actorState, badgeText) {
    const cls = actorState === "RUNNING" ? "status-success" :
      actorState === "SUSPENDED" ? "status-warning" : "status-neutral";
    return el("span", "status-pill " + cls, badgeText || actorState || "Suspended · 0 vCPU");
  }

  function renderSessionsView() {
    const backendPill = document.getElementById("sessions-backend-pill");
    if (backendPill) {
      backendPill.textContent = "Zero-wake · " + (state.ledgerBackend === "gemini-agent-platform"
        ? "Gemini Enterprise Agent Platform"
        : "Governed broker session ledger");
    }

    const countPill = document.getElementById("sessions-count-pill");
    if (countPill) {
      const n = state.ledgerSessions.length;
      countPill.textContent = n + (n === 1 ? " session" : " sessions");
    }

    const tbody = document.getElementById("sessions-ledger-body");
    if (tbody) {
      if (state.ledgerSessions.length === 0) {
        const tr = document.createElement("tr");
        const td = el("td", "secondary-copy", "No sessions persisted in the Session Ledger yet. Complete WORKSHOP(module-2) onto sre-agent-v2 and run an incident turn to populate Gemini Sessions.");
        td.colSpan = 7;
        tr.appendChild(td);
        tbody.replaceChildren(tr);
      } else {
        const rows = state.ledgerSessions.map((s) => {
          const tr = document.createElement("tr");
          const computeTd = document.createElement("td");
          computeTd.appendChild(makeComputeBadgePill(s.actor_state, s.compute_badge));

          const actionsTd = el("td", "row-actions");
          const inspectBtn = el(
            "button",
            "stroked-btn" + (s.session_id === state.selectedLedgerSessionID ? " active" : ""),
            "Inspect events"
          );
          inspectBtn.type = "button";
          inspectBtn.addEventListener("click", () => inspectLedgerSession(s.session_id));
          actionsTd.appendChild(inspectBtn);

          if (s.actor_state === "RUNNING" && s.actor_name) {
            const suspBtn = el("button", "stroked-btn", "Suspend");
            suspBtn.type = "button";
            suspBtn.addEventListener("click", async () => {
              await apiFetch("/api/actors/" + encodeURIComponent(s.actor_name) + "/suspend", { method: "POST" });
              await refreshLedgerAndMemories();
            });
            actionsTd.appendChild(suspBtn);
          }

          tr.append(
            el("td", "mono nowrap-cell", s.session_id),
            el("td", "mono nowrap-cell", s.incident_id || "-"),
            el("td", "mono nowrap-cell", s.template || "sre-agent-v2"),
            computeTd,
            el("td", "mono nowrap-cell", String(s.event_count || 0) + " events"),
            el("td", "mono nowrap-cell", s.updated_at || "-"),
            actionsTd
          );
          return tr;
        });
        tbody.replaceChildren(...rows);
      }
    }

    renderSessionEventInspector();
    renderMemoriesPanel();
  }

  function renderSessionEventInspector() {
    const titleEl = document.getElementById("session-inspector-title");
    const badgeEl = document.getElementById("session-inspector-badge");
    const stateGrid = document.getElementById("session-state-grid");
    const listBox = document.getElementById("session-events-list");
    if (!titleEl || !badgeEl || !stateGrid || !listBox) {
      return;
    }

    const detail = state.selectedLedgerDetail;
    if (!detail) {
      titleEl.textContent = "Turn event inspector";
      badgeEl.textContent = "Zero-wake ready";
      badgeEl.className = "status-pill status-neutral";
      stateGrid.classList.add("hidden");
      stateGrid.replaceChildren();
      listBox.replaceChildren(
        el("p", "secondary-copy", "Select a session row above to inspect structured prompts, tool calls, and state deltas while its Incident Actor stays suspended at 0 vCPU.")
      );
      return;
    }

    titleEl.textContent = "Turn event inspector · " + detail.session_id;
    badgeEl.textContent = detail.compute_badge || "Suspended · 0 vCPU";
    badgeEl.className = "status-pill " + (
      detail.actor_state === "RUNNING" ? "status-success" :
      detail.actor_state === "SUSPENDED" ? "status-warning" : "status-neutral"
    );

    const stateEntries = Object.entries(detail.state || {});
    if (stateEntries.length > 0) {
      stateGrid.classList.remove("hidden");
      stateGrid.replaceChildren(
        ...stateEntries.map(([k, v]) => makeKV("State · " + k, typeof v === "string" ? v : JSON.stringify(v)))
      );
    } else {
      stateGrid.classList.add("hidden");
      stateGrid.replaceChildren();
    }

    const events = detail.events || [];
    if (events.length === 0) {
      listBox.replaceChildren(el("p", "secondary-copy", "Session created with 0 persisted events."));
      return;
    }

    const items = events.map((ev, idx) => {
      const det = document.createElement("details");
      det.className = "tool-step-details";
      if (idx === 0 || idx === events.length - 1) {
        det.open = true;
      }
      const sum = document.createElement("summary");
      sum.className = "tool-step-summary";
      const kindLabel = formatEventKindLabel(ev.kind);
      sum.textContent = "#" + (idx + 1) + " · " + kindLabel + " · " + (ev.author || "agent") + " · " + (ev.summary || ev.id);

      const lines = [
        "event_id: " + (ev.id || "-") + "  invocation_id: " + (ev.invocation_id || "-") + "  timestamp: " + (ev.timestamp || "-")
      ];
      if (ev.text) {
        lines.push("\ntext:\n" + ev.text);
      }
      for (const tc of ev.tool_calls || []) {
        lines.push("\ntool_call: " + tc.name + (tc.id ? " [" + tc.id + "]" : "") + "\nargs: " + (tc.payload || "{}"));
      }
      for (const tr of ev.tool_responses || []) {
        lines.push("\ntool_response: " + tr.name + (tr.id ? " [" + tr.id + "]" : "") + "\nresult: " + (tr.payload || "{}"));
      }
      if (ev.state_delta && Object.keys(ev.state_delta).length > 0) {
        lines.push("\nstate_delta:\n" + JSON.stringify(ev.state_delta, null, 2));
      }

      const pre = el("pre", "code-block", lines.join("\n"));
      det.append(sum, pre);
      return det;
    });
    listBox.replaceChildren(...items);
  }

  function formatEventKindLabel(kind) {
    switch (kind) {
      case "prompt":
        return "Prompt";
      case "tool_call":
        return "Tool call";
      case "tool_response":
        return "Tool response";
      case "state_delta":
        return "State delta";
      case "agent_reply":
        return "Agent reply";
      case "compaction":
        return "Compaction summary";
      default:
        return "Event";
    }
  }

  function renderMemoriesPanel() {
    const countPill = document.getElementById("memories-count-pill");
    if (countPill) {
      const n = state.memories.length;
      countPill.textContent = n + (n === 1 ? " fact" : " facts");
    }

    const tbody = document.getElementById("memories-table-body");
    if (!tbody) {
      return;
    }
    if (state.memories.length === 0) {
      const tr = document.createElement("tr");
      const msg = state.memoryQuery
        ? "No Consolidated Memory facts matched query \"" + state.memoryQuery + "\"."
        : "No Consolidated Memory facts extracted yet. Run an incident turn on sre-agent-v2 to extract root-cause facts via AddSessionToMemory.";
      const td = el("td", "secondary-copy", msg);
      td.colSpan = 4;
      tr.appendChild(td);
      tbody.replaceChildren(tr);
      return;
    }

    const rows = state.memories.map((m) => {
      const tr = document.createElement("tr");
      const topicsTd = document.createElement("td");
      const topicsWrap = el("div", "chip-row");
      for (const t of m.topics || []) {
        topicsWrap.appendChild(el("span", "stroked-pill mono", t));
      }
      topicsTd.appendChild(topicsWrap);
      tr.append(
        el("td", "", m.fact || ""),
        el("td", "mono nowrap-cell", m.source_session_id || "-"),
        topicsTd,
        el("td", "mono nowrap-cell", m.timestamp || "-")
      );
      return tr;
    });
    tbody.replaceChildren(...rows);
  }

  async function refreshCatalog() {
    const catalogData = await apiFetch("/api/catalog").catch(() => ({
      backend: "broker-skill-registry",
      agent_card: null,
      agents: [],
      mcp_servers: [],
      skills: [],
      capabilities: []
    }));
    state.catalogBackend = catalogData.backend || "broker-skill-registry";
    state.catalogAgentCard = catalogData.agent_card || null;
    state.catalogAgents = Array.isArray(catalogData.agents) ? catalogData.agents : [];
    state.catalogMCPServers = Array.isArray(catalogData.mcp_servers) ? catalogData.mcp_servers : [];
    state.catalogSkills = Array.isArray(catalogData.skills) ? catalogData.skills : [];
    state.catalogCapabilities = Array.isArray(catalogData.capabilities) ? catalogData.capabilities : [];
    if (!state.selectedSkillName && state.catalogSkills.length > 0) {
      state.selectedSkillName = state.catalogSkills[0].name;
    }
    renderCatalogTable();
  }

  async function promoteSkillDefaultRevision(skillName, rev) {
    try {
      await apiFetch("/api/catalog/skills", {
        method: "POST",
        body: JSON.stringify({ skill: skillName, default_revision: rev })
      });
      showToast("Promoted " + skillName + " defaultRevision to " + rev + " (zero container rebuild).");
      await refreshCatalog();
    } catch (err) {
      showToast("Failed to update defaultRevision: " + err.message);
    }
  }

  function renderCatalogTable() {
    const backendPill = document.getElementById("catalog-backend-pill");
    if (backendPill) {
      backendPill.textContent = state.catalogBackend === "agent-registry-cloud"
        ? "Cloud Skill Registry · Workload Identity"
        : "Governed Broker Skill Registry · SHA-256 verified";
    }

    const skillsCount = document.getElementById("skills-count-pill");
    if (skillsCount) {
      const n = state.catalogSkills.length;
      skillsCount.textContent = n + (n === 1 ? " skill" : " skills");
    }

    const skillsBody = document.getElementById("skills-registry-body");
    if (skillsBody) {
      if (state.catalogSkills.length === 0) {
        const tr = document.createElement("tr");
        const td = el("td", "secondary-copy", "No Procedural Skills registered yet. Run `make publish-skill REV=v1` to publish ledger-pool-triage.");
        td.colSpan = 6;
        tr.appendChild(td);
        skillsBody.replaceChildren(tr);
      } else {
        const rows = state.catalogSkills.map((sk) => {
          const tr = document.createElement("tr");
          const revTd = document.createElement("td");
          revTd.appendChild(el("span", "status-pill status-success mono", sk.default_revision || "v1"));

          const shaTd = document.createElement("td");
          const shaShort = sk.active_sha256 ? "sha256:" + sk.active_sha256.slice(0, 16) + "…" : "-";
          const shaPill = el("span", "stroked-pill mono sha-pill", shaShort);
          if (sk.active_sha256) {
            shaPill.title = "SHA-256: " + sk.active_sha256;
          }
          shaTd.appendChild(shaPill);

          const revList = sk.revisions || [];
          const revCountText = revList.length + (revList.length === 1 ? " revision" : " revisions");

          const actionsTd = el("td", "row-actions");
          const isSelected = state.skillDrawerOpen && state.selectedSkillName === sk.name;
          const inspectBtn = el("button", "stroked-btn" + (isSelected ? " active" : ""), "Inspect revisions");
          inspectBtn.type = "button";
          inspectBtn.addEventListener("click", () => {
            state.selectedSkillName = sk.name;
            state.skillDrawerOpen = true;
            renderCatalogTable();
          });
          actionsTd.appendChild(inspectBtn);

          tr.append(
            el("td", "mono nowrap-cell", sk.name),
            revTd,
            shaTd,
            el("td", "mono nowrap-cell", revCountText),
            el("td", "", sk.description || ""),
            actionsTd
          );
          return tr;
        });
        skillsBody.replaceChildren(...rows);
      }
    }

    renderSkillRevisionDrawer();
    renderAgentRegistryTables();

    const tbody = document.getElementById("catalog-table-body");
    if (!tbody) {
      return;
    }
    const caps = state.catalogCapabilities && state.catalogCapabilities.length > 0
      ? state.catalogCapabilities
      : [
          { capability: "clone_repo", kind: "ADK functiontool", boundary: "Routes Smart HTTP git clone strictly via sre-gateway/ops", stage: "Module 01 (active)" },
          { capability: "read_workspace_file", kind: "ADK functiontool", boundary: "Confined to /workspace with filepath.Clean + symlink rejection", stage: "Module 01 (active)" },
          { capability: "run_workspace_command", kind: "ADK functiontool", boundary: "Allowlists ledger-verify, git status/diff/log, and .skills/*.sh", stage: "Module 01 (active)" },
          { capability: "stage_git_patch", kind: "ADK functiontool", boundary: "Runs ledger-verify AST + YAML checks before committing branch", stage: "Module 01 (active)" },
          { capability: "load_memory & preload_memory", kind: "Gemini Sessions & Memory Bank", boundary: "Keyless /agentengine proxy + zero-wake Session Ledger & cross-rollout recall", stage: "Module 02 (active)" },
          { capability: "ledger-pool-triage (v1 / v2)", kind: "Procedural Skill (Skill Registry)", boundary: "SHA-256 archive verification + zero Golden Snapshot churn", stage: "Module 03 (active)" },
          { capability: "push_git_branch & apply_remediation_pr", kind: "Remediation Gate (ADK toolconfirmation)", boundary: "0 vCPU Cloud Storage suspend + human operator approval before merge", stage: "Module 04 (active)" },
          { capability: "Cloud Logging, GKE & ops-hub MCP", kind: "Agent Registry MCP & A2A", boundary: "readOnlyHint / destructiveHint enforcement + A2A Agent Card", stage: "Module 04 (active)" }
        ];
    tbody.replaceChildren(...caps.map((item) => {
      const tr = document.createElement("tr");
      tr.append(
        el("td", "mono nowrap-cell", item.capability),
        el("td", "nowrap-cell", item.kind),
        el("td", "", item.boundary),
        el("td", "nowrap-cell", item.stage)
      );
      return tr;
    }));
  }

  function firstAgentInterfaceURL(ag) {
    const protos = Array.isArray(ag && ag.protocols) ? ag.protocols : [];
    for (const p of protos) {
      const ifaces = Array.isArray(p && p.interfaces) ? p.interfaces : [];
      for (const iface of ifaces) {
        if (iface && iface.url) return iface.url;
      }
    }
    return ag && ag.card && ag.card.content && ag.card.content.url ? ag.card.content.url : "-";
  }

  function firstAgentProtocolType(ag) {
    const protos = Array.isArray(ag && ag.protocols) ? ag.protocols : [];
    return protos.length > 0 && protos[0] && protos[0].type ? protos[0].type : "";
  }

  function isGKEAutoDiscoveredAgent(ag) {
    return Boolean(ag && typeof ag.source === "string" && ag.source.includes("gke-auto-discovery"));
  }

  function renderAgentRegistryTables() {
    const a2aCount = document.getElementById("a2a-count-pill");
    const a2aBody = document.getElementById("a2a-agents-body");
    const agents = state.catalogAgents || [];
    if (a2aCount) {
      const n = agents.length;
      a2aCount.textContent = n + (n === 1 ? " agent" : " agents");
    }
    if (a2aBody) {
      if (agents.length === 0) {
        const tr = document.createElement("tr");
        const td = el("td", "secondary-copy", "No A2A agents registered yet.");
        td.colSpan = 5;
        tr.appendChild(td);
        a2aBody.replaceChildren(tr);
      } else {
        a2aBody.replaceChildren(...agents.map((ag) => {
          const tr = document.createElement("tr");
          const gkeAuto = isGKEAutoDiscoveredAgent(ag);
          const cardURL = ag.card && ag.card.content && ag.card.content.url ? ag.card.content.url : "";
          const nameTd = document.createElement("td");
          nameTd.append(
            el("strong", "", ag.displayName || ag.name || "Agent"),
            el("div", "mono secondary-copy break-cell", gkeAuto && cardURL ? "/.well-known/agent-card.json" : (ag.name || ""))
          );

          const endpointURL = firstAgentInterfaceURL(ag);
          const discTd = document.createElement("td");
          const discWrap = el("div", "chip-row");
          if (gkeAuto) {
            discWrap.append(
              el("span", "status-pill status-success mono", "registry.gke.io/functional-type: AGENT"),
              el("span", "stroked-pill mono", "a2a-protocol.org/agent-card")
            );
          } else {
            discWrap.appendChild(
              el("span", "status-pill status-neutral mono", ag.source || "agent-registry-cloud")
            );
            const protoType = firstAgentProtocolType(ag);
            if (protoType) {
              discWrap.appendChild(el("span", "stroked-pill mono", protoType));
            }
          }
          discTd.appendChild(discWrap);

          const skillsTd = document.createElement("td");
          const skillsWrap = el("div", "chip-row");
          const skills = Array.isArray(ag.skills) ? ag.skills : [];
          if (skills.length === 0) {
            skillsWrap.appendChild(el("span", "secondary-copy mono", "-"));
          } else {
            for (const sk of skills) {
              skillsWrap.appendChild(el("span", "stroked-pill mono", sk.id || sk.name || "skill"));
            }
          }
          skillsTd.appendChild(skillsWrap);

          tr.append(
            nameTd,
            el("td", "mono nowrap-cell", ag.version || "-"),
            el("td", "mono break-cell", endpointURL),
            discTd,
            skillsTd
          );
          return tr;
        }));
      }
    }

    const mcpCount = document.getElementById("mcp-count-pill");
    const mcpBody = document.getElementById("mcp-servers-body");
    const servers = state.catalogMCPServers || [];
    if (mcpCount) {
      const n = servers.length;
      mcpCount.textContent = n + (n === 1 ? " server" : " servers");
    }
    if (mcpBody) {
      if (servers.length === 0) {
        const tr = document.createElement("tr");
        const td = el("td", "secondary-copy", "No MCP servers discovered yet.");
        td.colSpan = 4;
        tr.appendChild(td);
        mcpBody.replaceChildren(tr);
      } else {
        mcpBody.replaceChildren(...servers.map((srv) => {
          const tr = document.createElement("tr");
          const idTd = document.createElement("td");
          idTd.append(
            el("strong", "mono nowrap-cell", srv.mcpServerId || srv.displayName || "mcp-server"),
            el("div", "secondary-copy", srv.displayName || "")
          );

          const ifaceURL = Array.isArray(srv.interfaces) && srv.interfaces.length > 0
            ? (srv.interfaces[0].url || "")
            : "";

          const toolsTd = document.createElement("td");
          const toolsWrap = el("div", "chip-row");
          for (const tl of srv.tools || []) {
            const ann = tl.annotations || {};
            const isDestructive = Boolean(ann.destructiveHint);
            const isReadOnly = Boolean(ann.readOnlyHint) && !isDestructive;
            const hintLabel = isDestructive ? "destructiveHint" : (isReadOnly ? "readOnlyHint" : "tool");
            const badgeCls = isDestructive ? "status-error" : (isReadOnly ? "status-success" : "status-neutral");
            const pill = el("span", "status-pill mono " + badgeCls, tl.name + " · " + hintLabel);
            pill.title = tl.description || tl.name;
            toolsWrap.appendChild(pill);
          }
          toolsTd.appendChild(toolsWrap);

          tr.append(
            idTd,
            el("td", "mono break-cell", ifaceURL),
            toolsTd,
            el("td", "", srv.description || "")
          );
          return tr;
        }));
      }
    }
  }

  function renderSkillRevisionDrawer() {
    const drawer = document.getElementById("skill-revision-drawer");
    if (!drawer) {
      return;
    }
    const sk = state.catalogSkills.find((s) => s.name === state.selectedSkillName) || state.catalogSkills[0];
    if (!state.skillDrawerOpen || !sk) {
      drawer.classList.add("hidden");
      return;
    }
    drawer.classList.remove("hidden");
    document.getElementById("skill-drawer-title").textContent = "Skill revisions · " + sk.name;
    document.getElementById("skill-drawer-default-pill").textContent = "default: " + (sk.default_revision || "v1");
    document.getElementById("skill-drawer-urn").textContent = sk.resource_name || "";

    const listHost = document.getElementById("skill-revisions-list");
    const cards = (sk.revisions || []).map((rv) => {
      const card = el("div", "revision-card" + (rv.active ? " active-revision" : ""));
      const hdr = el("div", "card-header");
      const left = el("div", "action-bar-left");
      left.append(
        el("strong", "mono", sk.name + "@" + rv.revision),
        el(
          "span",
          "status-pill " + (rv.active ? "status-success" : "status-neutral"),
          rv.active ? "Active defaultRevision" : "Immutable snapshot"
        ),
        el("span", "stroked-pill mono", "sha256:" + (rv.sha256 || ""))
      );
      hdr.appendChild(left);

      if (!rv.active) {
        const promoteBtn = el("button", "stroked-btn", "Set as default");
        promoteBtn.type = "button";
        promoteBtn.addEventListener("click", () => promoteSkillDefaultRevision(sk.name, rv.revision));
        hdr.appendChild(promoteBtn);
      }
      card.appendChild(hdr);

      const metaRow = el("div", "chip-row");
      metaRow.appendChild(el("span", "stroked-pill mono", String(rv.size_bytes || 0) + " bytes"));
      for (const f of rv.files || []) {
        metaRow.appendChild(el("span", "stroked-pill mono", f));
      }
      card.appendChild(metaRow);

      if (rv.frontmatter && rv.frontmatter.description) {
        const descP = el("p", "body-copy secondary-copy", rv.frontmatter.description);
        descP.style.marginTop = "8px";
        card.appendChild(descP);
      }
      if (rv.instructions_preview) {
        card.appendChild(el("pre", "code-block", rv.instructions_preview));
      }
      return card;
    });
    listHost.replaceChildren(...cards);
  }

  async function loadArchitecture() {
    if (!state.architecture) {
      state.architecture = await apiFetch("/api/architecture").catch(() => null);
    }
    if (!state.architecture) {
      return;
    }
    renderArchitectureView();
  }

  function renderArchitectureView() {
    const cat = state.architecture;
    if (!cat) {
      return;
    }
    document.getElementById("arch-title").textContent = cat.title || "Architecture";

    const flowBtnRow = document.getElementById("arch-flow-buttons");
    const flowBtns = (cat.flows || []).map((f) => {
      const btn = el("button", "stroked-btn" + (f.id === state.selectedFlowID ? " active" : ""), f.title);
      btn.type = "button";
      btn.addEventListener("click", () => {
        state.selectedFlowID = f.id;
        state.activeHopIndex = 0;
        renderArchitectureView();
      });
      return btn;
    });
    flowBtnRow.replaceChildren(...flowBtns);

    const activeFlow = (cat.flows || []).find((f) => f.id === state.selectedFlowID) || (cat.flows || [])[0];
    document.getElementById("arch-flow-caption").textContent = activeFlow ? activeFlow.description : "";

    renderArchitectureSVG(cat, activeFlow);
    renderArchitectureInspector(cat);

    const modBody = document.getElementById("arch-modules-body");
    modBody.replaceChildren(...(cat.modules || []).map((m) => {
      const tr = document.createElement("tr");
      tr.append(
        el("td", "mono nowrap-cell", "Module " + m.number),
        el("td", "nowrap-cell", m.title),
        el("td", "", m.summary)
      );
      return tr;
    }));

    if (state.hopTimer) {
      window.clearInterval(state.hopTimer);
    }
    if (activeFlow && activeFlow.hops && activeFlow.hops.length > 0) {
      state.hopTimer = window.setInterval(() => {
        if (state.activeRoute !== "/about") {
          return;
        }
        state.activeHopIndex = (state.activeHopIndex + 1) % activeFlow.hops.length;
        renderArchitectureSVG(cat, activeFlow);
      }, 2200);
    }
  }

  function makeArrowMarker(id, cls) {
    const marker = document.createElementNS(SVG_NS, "marker");
    marker.setAttribute("id", id);
    marker.setAttribute("viewBox", "0 0 10 10");
    marker.setAttribute("refX", "8");
    marker.setAttribute("refY", "5");
    marker.setAttribute("markerWidth", "6");
    marker.setAttribute("markerHeight", "6");
    marker.setAttribute("orient", "auto-start-reverse");
    const p = document.createElementNS(SVG_NS, "path");
    p.setAttribute("d", "M 0 1.5 L 8.5 5 L 0 8.5 z");
    p.setAttribute("class", cls);
    marker.appendChild(p);
    return marker;
  }

  function edgeRoutePoints(edge, from, to) {
    switch (edge.id) {
      case "op-to-gw":
      case "gw-to-broker":
      case "router-to-actor":
      case "actor-to-gcs":
        return [
          { x: from.x + from.width / 2, y: from.y + from.height },
          { x: to.x + to.width / 2, y: to.y }
        ];
      case "broker-to-router":
        return [
          { x: from.x + from.width, y: from.y + 34 },
          { x: 350, y: from.y + 34 },
          { x: 350, y: to.y + 46 },
          { x: to.x, y: to.y + 46 }
        ];
      case "actor-to-broker":
        return [
          { x: from.x, y: from.y + 56 },
          { x: 360, y: from.y + 56 },
          { x: 360, y: to.y + 72 },
          { x: to.x + to.width, y: to.y + 72 }
        ];
      case "broker-to-platform":
        return [
          { x: from.x + from.width, y: from.y + 102 },
          { x: 352, y: from.y + 102 },
          { x: 352, y: 462 },
          { x: 708, y: 462 },
          { x: 708, y: to.y + 92 },
          { x: to.x, y: to.y + 92 }
        ];
      case "broker-to-sim":
        return [
          { x: from.x + from.width, y: from.y + 124 },
          { x: 344, y: from.y + 124 },
          { x: 344, y: 474 },
          { x: 718, y: 474 },
          { x: 718, y: to.y + 68 },
          { x: to.x, y: to.y + 68 }
        ];
      default:
        return [nodeCenter(from), nodeCenter(to)];
    }
  }

  function pointsToSVGPath(pts) {
    if (!pts || pts.length === 0) {
      return "";
    }
    return pts.map((pt, i) => (i === 0 ? "M " : "L ") + pt.x + " " + pt.y).join(" ");
  }

  function edgeMidpoint(edge, pts) {
    if (!pts || pts.length < 2) {
      return { x: 540, y: 250 };
    }
    if (pts.length === 2) {
      return {
        x: (pts[0].x + pts[1].x) / 2,
        y: (pts[0].y + pts[1].y) / 2
      };
    }
    if (edge.id === "broker-to-router") {
      return { x: 350, y: 224 };
    }
    if (edge.id === "actor-to-broker") {
      return { x: 360, y: 320 };
    }
    if (edge.id === "broker-to-platform") {
      return { x: 534, y: 462 };
    }
    if (edge.id === "broker-to-sim") {
      return { x: 534, y: 474 };
    }
    const midIdx = Math.floor(pts.length / 2);
    return {
      x: (pts[midIdx - 1].x + pts[midIdx].x) / 2,
      y: (pts[midIdx - 1].y + pts[midIdx].y) / 2
    };
  }

  function renderArchitectureSVG(cat, activeFlow) {
    const host = document.getElementById("arch-svg-host");
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("id", "arch-svg");
    svg.setAttribute("viewBox", "0 0 1080 500");
    svg.setAttribute("class", "arch-svg");

    const defs = document.createElementNS(SVG_NS, "defs");
    defs.appendChild(makeArrowMarker("arch-arrow", "svg-edge-arrow"));
    defs.appendChild(makeArrowMarker("arch-arrow-active", "svg-edge-arrow-active"));
    svg.appendChild(defs);

    const nodesByID = {};
    for (const n of cat.nodes || []) {
      nodesByID[n.id] = n;
    }

    for (const z of cat.zones || []) {
      const rect = document.createElementNS(SVG_NS, "rect");
      rect.setAttribute("x", String(z.x));
      rect.setAttribute("y", String(z.y));
      rect.setAttribute("width", String(z.width));
      rect.setAttribute("height", String(z.height));
      rect.setAttribute("rx", "4");
      rect.setAttribute("class", "svg-zone-rect");
      svg.appendChild(rect);

      const title = document.createElementNS(SVG_NS, "text");
      title.setAttribute("x", String(z.x + 14));
      title.setAttribute("y", String(z.y + 24));
      title.setAttribute("class", "svg-zone-title");
      title.textContent = z.title;
      svg.appendChild(title);

      const sub = document.createElementNS(SVG_NS, "text");
      sub.setAttribute("x", String(z.x + 14));
      sub.setAttribute("y", String(z.y + 42));
      sub.setAttribute("class", "svg-zone-sub");
      sub.textContent = z.subtitle;
      svg.appendChild(sub);
    }

    const activeHop = activeFlow && activeFlow.hops ? activeFlow.hops[state.activeHopIndex] : null;
    let activeEdgeObj = null;
    let activeEdgePts = null;

    for (const edge of cat.edges || []) {
      const from = nodesByID[edge.from];
      const to = nodesByID[edge.to];
      if (!from || !to) {
        continue;
      }
      const pts = edgeRoutePoints(edge, from, to);
      const isActive = Boolean(activeHop && activeHop.edge_id === edge.id);
      if (isActive) {
        activeEdgeObj = edge;
        activeEdgePts = pts;
      }
      const pathEl = document.createElementNS(SVG_NS, "path");
      pathEl.setAttribute("d", pointsToSVGPath(pts));
      pathEl.setAttribute("class", "svg-edge-line" + (isActive ? " active-edge" : ""));
      pathEl.setAttribute("marker-end", isActive ? "url(#arch-arrow-active)" : "url(#arch-arrow)");
      svg.appendChild(pathEl);
    }

    for (const n of cat.nodes || []) {
      const g = document.createElementNS(SVG_NS, "g");
      const isEndpoint = Boolean(activeEdgeObj && (activeEdgeObj.from === n.id || activeEdgeObj.to === n.id));
      g.setAttribute("data-node-id", n.id);
      g.setAttribute(
        "class",
        "svg-node-group" +
          (n.id === state.selectedNodeID ? " selected" : "") +
          (isEndpoint ? " active-endpoint" : "")
      );
      g.addEventListener("click", () => {
        state.selectedNodeID = n.id;
        renderArchitectureInspector(cat);
        renderArchitectureSVG(cat, activeFlow);
      });

      const rect = document.createElementNS(SVG_NS, "rect");
      rect.setAttribute("x", String(n.x));
      rect.setAttribute("y", String(n.y));
      rect.setAttribute("width", String(n.width));
      rect.setAttribute("height", String(n.height));
      rect.setAttribute("rx", "4");
      rect.setAttribute("class", "svg-node-rect svg-node");
      g.appendChild(rect);

      const t = document.createElementNS(SVG_NS, "text");
      t.setAttribute("x", String(n.x + 12));
      t.setAttribute("y", String(n.y + 24));
      t.setAttribute("class", "svg-node-title");
      t.textContent = n.title;
      g.appendChild(t);

      const s = document.createElementNS(SVG_NS, "text");
      s.setAttribute("x", String(n.x + 12));
      s.setAttribute("y", String(n.y + 44));
      s.setAttribute("class", "svg-node-sub");
      s.textContent = n.subtitle;
      g.appendChild(s);

      const b = document.createElementNS(SVG_NS, "text");
      b.setAttribute("x", String(n.x + 12));
      b.setAttribute("y", String(n.y + 66));
      b.setAttribute("class", "svg-node-badge");
      b.textContent = n.badge;
      g.appendChild(b);

      svg.appendChild(g);
    }

    if (activeHop && activeEdgeObj && activeEdgePts) {
      const overlay = document.createElementNS(SVG_NS, "g");
      overlay.setAttribute("class", "svg-overlay-group");

      const mid = edgeMidpoint(activeEdgeObj, activeEdgePts);
      const dot = document.createElementNS(SVG_NS, "circle");
      dot.setAttribute("cx", String(mid.x));
      dot.setAttribute("cy", String(mid.y));
      dot.setAttribute("r", "5");
      dot.setAttribute("class", "svg-pulse-dot");
      overlay.appendChild(dot);

      const labelStr = "Step " + activeHop.step + " · " + (activeEdgeObj.protocol || activeEdgeObj.label);
      const pillW = Math.max(128, labelStr.length * 6.8 + 20);
      const pillH = 22;
      const pillX = Math.max(24, Math.min(1056 - pillW, mid.x - pillW / 2));
      const pillY = Math.max(12, Math.min(474, mid.y - 26));

      const pillBg = document.createElementNS(SVG_NS, "rect");
      pillBg.setAttribute("x", String(pillX));
      pillBg.setAttribute("y", String(pillY));
      pillBg.setAttribute("width", String(pillW));
      pillBg.setAttribute("height", String(pillH));
      pillBg.setAttribute("rx", "11");
      pillBg.setAttribute("class", "svg-step-pill-bg");
      overlay.appendChild(pillBg);

      const pillTxt = document.createElementNS(SVG_NS, "text");
      pillTxt.setAttribute("x", String(pillX + pillW / 2));
      pillTxt.setAttribute("y", String(pillY + 15));
      pillTxt.setAttribute("text-anchor", "middle");
      pillTxt.setAttribute("class", "svg-step-pill-text");
      pillTxt.textContent = labelStr;
      overlay.appendChild(pillTxt);

      svg.appendChild(overlay);
    }

    host.replaceChildren(svg);

    const hopStrip = document.getElementById("arch-hop-strip");
    if (activeFlow && activeFlow.hops) {
      hopStrip.replaceChildren(...activeFlow.hops.map((h, idx) => {
        const card = el("div", "hop-card" + (idx === state.activeHopIndex ? " active" : ""));
        card.append(
          el("strong", "mono", "Step " + h.step),
          el("p", "turn-text", h.caption)
        );
        card.addEventListener("click", () => {
          state.activeHopIndex = idx;
          renderArchitectureSVG(cat, activeFlow);
        });
        return card;
      }));
    } else {
      hopStrip.replaceChildren();
    }
  }

  function nodeCenter(n) {
    return { x: n.x + n.width / 2, y: n.y + n.height / 2 };
  }

  function renderArchitectureInspector(cat) {
    const node = (cat.nodes || []).find((n) => n.id === state.selectedNodeID) || (cat.nodes || [])[0];
    if (!node) {
      return;
    }
    document.getElementById("arch-node-title").textContent = node.title;
    document.getElementById("arch-node-badge").textContent = node.badge;
    document.getElementById("arch-node-subtitle").textContent = node.subtitle;
    document.getElementById("arch-node-summary").textContent = node.summary;
    document.getElementById("arch-node-invariants").replaceChildren(
      ...(node.invariants || []).map((inv) => el("li", "", inv))
    );
  }

  function openCmdK(open) {
    const backdrop = document.getElementById("cmdk-backdrop");
    if (!backdrop) {
      return;
    }
    backdrop.classList.toggle("hidden", !open);
    if (open && state.sessionInfo) {
      const info = state.sessionInfo;
      document.getElementById("cmdk-env-grid").replaceChildren(
        makeKV("Atespace", info.atespace),
        makeKV("Gemini model", info.gemini_model),
        makeKV("MOCK_LLM", String(info.mock_llm)),
        makeKV("Idle suspend after", info.idle_after),
        makeKV("SPIFFE mode", info.spiffe_mode + " (" + info.spiffe_domain + ")"),
        makeKV("Default template", info.default_template)
      );
      renderCmdKOAuthRow((state.governance && state.governance.oauth_3lo) || {});
      const presets = document.getElementById("cmdk-presets");
      presets.replaceChildren(...PROMPT_PRESETS.map((preset) => {
        const btn = el("button", "starter-chip", preset.prompt);
        btn.type = "button";
        btn.addEventListener("click", async () => {
          setRoute("/", true);
          openCmdK(false);
          await applyPresetPrompt(preset);
        });
        return btn;
      }));
    }
  }

  async function bootstrap() {
    hydrateStaticIcons();
    applyTheme(document.documentElement.getAttribute("data-theme") || "light", false);

    try {
      const sess = await apiFetch("/api/session");
      state.csrfToken = sess.csrf_token || "";
      state.sessionInfo = sess;
      document.getElementById("project-pill").textContent = sess.project_id || "workshop-project";
      document.getElementById("spiffe-pill").textContent = "SPIFFE · " + (sess.spiffe_mode || "wif");
      if (sess.theme) {
        applyTheme(sess.theme, false);
      }
    } catch (_err) {
      // Offline preview fallback.
    }

    document.getElementById("btn-nav-toggle").addEventListener("click", () => {
      document.getElementById("left-nav").classList.toggle("collapsed");
    });
    document.getElementById("btn-theme-light").addEventListener("click", () => applyTheme("light", true));
    document.getElementById("btn-theme-dark").addEventListener("click", () => applyTheme("dark", true));

    document.querySelectorAll(".nav-item").forEach((link) => {
      link.addEventListener("click", (ev) => {
        ev.preventDefault();
        setRoute(link.getAttribute("data-route") || "/", true);
      });
    });
    window.addEventListener("popstate", () => setRoute(window.location.pathname, false));

    document.querySelectorAll(".tab-btn").forEach((btn) => {
      btn.addEventListener("click", () => setTab(btn.getAttribute("data-tab") || "overview"));
    });

    document.getElementById("incident-select").addEventListener("change", async (ev) => {
      state.selectedIncidentID = ev.target.value;
      const matchTrace = (state.traces || []).find((t) => t.incident_id === state.selectedIncidentID);
      if (matchTrace) {
        state.selectedTraceID = matchTrace.trace_id;
      }
      await refreshAll();
    });

    const traceSelect = document.getElementById("trace-select");
    if (traceSelect) {
      traceSelect.addEventListener("change", async (ev) => {
        state.selectedTraceID = ev.target.value;
        if (state.selectedTraceID) {
          const full = await apiFetch("/api/traces/" + encodeURIComponent(state.selectedTraceID)).catch(() => null);
          if (full && full.trace_id) {
            state.traces = (state.traces || []).map((t) => (t.trace_id === full.trace_id ? full : t));
          }
        }
        renderTracesTab();
      });
    }

    const runEvalsBtn = document.getElementById("btn-run-evals");
    if (runEvalsBtn) {
      runEvalsBtn.addEventListener("click", () => runOnlineEvals());
    }

    document.getElementById("btn-refresh").addEventListener("click", () => refreshAll());
    const refreshSessBtn = document.getElementById("btn-refresh-sessions");
    if (refreshSessBtn) {
      refreshSessBtn.addEventListener("click", () => refreshLedgerAndMemories());
    }
    const refreshCatBtn = document.getElementById("btn-refresh-catalog");
    if (refreshCatBtn) {
      refreshCatBtn.addEventListener("click", () => refreshCatalog());
    }
    const closeSkillDrawerBtn = document.getElementById("btn-close-skill-drawer");
    if (closeSkillDrawerBtn) {
      closeSkillDrawerBtn.addEventListener("click", () => {
        state.skillDrawerOpen = false;
        renderSkillRevisionDrawer();
      });
    }
    const memForm = document.getElementById("memory-search-form");
    if (memForm) {
      memForm.addEventListener("submit", (ev) => {
        ev.preventDefault();
        const input = document.getElementById("memory-search-input");
        state.memoryQuery = input ? input.value.trim() : "";
        refreshLedgerAndMemories();
      });
    }
    const memResetBtn = document.getElementById("btn-memory-reset");
    if (memResetBtn) {
      memResetBtn.addEventListener("click", () => {
        const input = document.getElementById("memory-search-input");
        if (input) {
          input.value = "";
        }
        state.memoryQuery = "";
        refreshLedgerAndMemories();
      });
    }
    document.getElementById("btn-suspend-actor").addEventListener("click", async () => {
      const actorName = actorNameForIncident(state.selectedIncidentID);
      try {
        await apiFetch("/api/actors/" + encodeURIComponent(actorName) + "/suspend", { method: "POST" });
        showToast("Suspended Incident Actor " + actorName + " to Cloud Storage (0 vCPU).");
        await refreshAll();
      } catch (err) {
        showToast("Suspend failed: " + err.message);
      }
    });

    document.getElementById("assist-form").addEventListener("submit", (ev) => {
      ev.preventDefault();
      submitChatTurn({});
    });
    document.getElementById("btn-clear-chat").addEventListener("click", () => {
      state.clearedIncidentChats[state.selectedIncidentID] = true;
      state.turnsByIncident[state.selectedIncidentID] = [];
      renderAssistStream();
    });

    async function decideRemediationGate(confirmed) {
      if (state.busy) {
        return;
      }
      const incID = state.selectedIncidentID;
      const inc = state.incidents.find((i) => i.id === incID);
      const serverGate = inc && inc.pending_gate ? inc.pending_gate : null;
      const callID = (serverGate && serverGate.call_id) || state.pendingConfirmationByIncident[incID] || "";
      if (!callID) {
        return;
      }

      const approveBtn = document.getElementById("btn-approve-gate");
      const rejectBtn = document.getElementById("btn-reject-gate");
      state.busy = true;
      if (approveBtn) {
        approveBtn.disabled = true;
      }
      if (rejectBtn) {
        rejectBtn.disabled = true;
      }

      if (!state.turnsByIncident[incID]) {
        state.turnsByIncident[incID] = [];
      }
      state.turnsByIncident[incID].push({
        role: "user",
        text: confirmed
          ? "Approved Remediation Gate confirmation (wake suspended actor & execute apply_remediation_pr)."
          : "Rejected Remediation Gate confirmation.",
        metaRight: incID
      });
      renderAssistStream();

      const actionPath = confirmed ? "/remediation/approve" : "/remediation/reject";
      try {
        const res = await apiFetch("/api/incidents/" + encodeURIComponent(incID) + actionPath, {
          method: "POST",
          body: JSON.stringify({ call_id: callID })
        });
        delete state.pendingConfirmationByIncident[incID];
        const resumeMs = res.resume_duration_ms || res.duration_ms || 0;
        state.turnsByIncident[incID].push({
          role: "model",
          text: res.reply || "(Completed remediation decision)",
          template: res.template,
          metaRight: "wake + run: " + resumeMs + " ms",
          toolSteps: res.tool_steps || []
        });
        showToast(confirmed
          ? "Approved remediation for " + incID + " (actor resumed in " + resumeMs + " ms)."
          : "Rejected remediation for " + incID + " without merging.");
        await refreshAll();
      } catch (err) {
        if (err && err.status === 401 && err.payload && err.payload.consent_required) {
          state.turnsByIncident[incID].push({
            role: "model",
            text: "3LO operator consent is required (" + (err.payload.provider || "ops-hub-3lo") +
              ") before waking the suspended Incident Actor to execute apply_remediation_pr. Click Authorize 3LO remediation in the banner above.",
            template: "Auth Manager",
            metaRight: "HTTP 401 · 3LO required"
          });
          showToast("Authorize 3LO remediation before approving merge.");
          await refreshAll();
        } else {
          showToast("Remediation decision failed: " + err.message);
          renderAssistStream();
        }
      } finally {
        state.busy = false;
        if (approveBtn) {
          approveBtn.disabled = false;
        }
        if (rejectBtn) {
          rejectBtn.disabled = false;
        }
      }
    }

    document.getElementById("btn-approve-gate").addEventListener("click", () => {
      decideRemediationGate(true);
    });
    document.getElementById("btn-reject-gate").addEventListener("click", () => {
      decideRemediationGate(false);
    });
    const gateAuthBtn = document.getElementById("btn-authorize-3lo");
    if (gateAuthBtn) {
      gateAuthBtn.addEventListener("click", () => start3LOConsent());
    }
    const govAuthBtn = document.getElementById("btn-gov-authorize-3lo");
    if (govAuthBtn) {
      govAuthBtn.addEventListener("click", () => start3LOConsent());
    }
    const govRevBtn = document.getElementById("btn-gov-revoke-3lo");
    if (govRevBtn) {
      govRevBtn.addEventListener("click", () => revoke3LOGrant());
    }
    const cmdkAuthBtn = document.getElementById("btn-cmdk-authorize-3lo");
    if (cmdkAuthBtn) {
      cmdkAuthBtn.addEventListener("click", () => start3LOConsent());
    }
    const cmdkRevBtn = document.getElementById("btn-cmdk-revoke-3lo");
    if (cmdkRevBtn) {
      cmdkRevBtn.addEventListener("click", () => revoke3LOGrant());
    }

    document.getElementById("btn-cmdk").addEventListener("click", () => openCmdK(true));
    document.getElementById("btn-close-cmdk").addEventListener("click", () => openCmdK(false));
    document.getElementById("cmdk-backdrop").addEventListener("click", (ev) => {
      if (ev.target.id === "cmdk-backdrop") {
        openCmdK(false);
      }
    });
    window.addEventListener("keydown", (ev) => {
      if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "k") {
        ev.preventDefault();
        const isHidden = document.getElementById("cmdk-backdrop").classList.contains("hidden");
        openCmdK(isHidden);
      } else if (ev.key === "Escape") {
        openCmdK(false);
        if (state.skillDrawerOpen) {
          state.skillDrawerOpen = false;
          renderSkillRevisionDrawer();
        }
      }
    });

    setRoute(window.location.pathname, false);
    await refreshAll();
  }

  document.addEventListener("DOMContentLoaded", bootstrap);
})();
