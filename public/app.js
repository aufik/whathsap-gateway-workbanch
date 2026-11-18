const state = {
  settings: null,
  ai: null,
  wa: null,
  runner: null,
  telemetry: null,
  summary: null,
  rows: [],
  rowMap: new Map(),
  filters: {
    search: "",
    opd: "",
    kategori: "",
    statusBank: "",
    result: "",
  },
};

const el = (id) => document.getElementById(id);

async function api(path, options = {}) {
  const opts = { ...options, headers: { ...(options.headers || {}) } };
  if (opts.body && !(opts.body instanceof FormData) && typeof opts.body === "object") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, opts);
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`);
  }
  return data;
}

function escapeHTML(str) {
  return String(str ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function formatReplyHTML(raw) {
  if (!raw || !raw.trim()) {
    return `<span class="reply-empty">Belum ada balasan</span>`;
  }
  let safe = escapeHTML(raw);
  safe = safe.replace(/\[Berkas\]/gi, `<span class="tag-media">[Berkas]</span>`);
  safe = safe.replace(/\[Koordinat\]/gi, `<span class="tag-media tag-coord">[Koordinat]</span>`);
  return `<div class="reply-pre">${safe}</div>`;
}

function toast(msg, isError = false) {
  const wrap = el("toastContainer");
  const item = document.createElement("div");
  item.className = `toast${isError ? " error" : ""}`;
  item.textContent = msg;
  wrap.appendChild(item);
  setTimeout(() => item.remove(), 3800);
}

/* Initial Load & SSE */
async function loadInitialState() {
  try {
    const snap = await api("/api/state");
    applyFullSnapshot(snap);
  } catch (err) {
    toast(`Gagal memuat state awal: ${err.message}`, true);
  }
}

function applyFullSnapshot(snap) {
  state.settings = snap.settings;
  state.ai = snap.ai;
  state.wa = snap.wa;
  state.runner = snap.runner;
  state.telemetry = snap.telemetry;
  state.summary = snap.summary;
  state.rows = snap.rows || [];
  state.rowMap.clear();
  for (const r of state.rows) {
    state.rowMap.set(r.no, r);
  }

  populateSettingsUI();
  populateFilterDropdowns();
  renderAIStatus();
  renderWAStatus();
  renderRunnerStatus();
  renderTelemetry();
  renderSummaryAndOPD();
  renderTable();
}

function connectSSE() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    if (!e.data) return;
    try {
      const evt = JSON.parse(e.data);
      handleSSEEvent(evt);
    } catch {
      // ignore malformed frame
    }
  };
  es.onerror = () => {
    es.close();
    setTimeout(connectSSE, 3000);
  };
}

function handleSSEEvent(evt) {
  switch (evt.type) {
    case "wa_status":
      state.wa = evt.payload;
      renderWAStatus();
      break;
    case "settings_updated":
      state.settings = evt.payload;
      populateSettingsUI();
      renderTelemetry();
      break;
    case "runner_status": {
      const prevRow = state.runner?.currentRowNo;
      state.runner = evt.payload;
      renderRunnerStatus();
      if (prevRow && prevRow !== state.runner.currentRowNo) {
        patchSingleTableRow(prevRow);
      }
      if (state.runner.currentRowNo) {
        patchSingleTableRow(state.runner.currentRowNo);
      }
      break;
    }
    case "human_behavior":
      state.telemetry = evt.payload;
      renderTelemetry();
      break;
    case "row_updated": {
      const r = evt.payload;
      state.rowMap.set(r.no, r);
      const idx = state.rows.findIndex((x) => x.no === r.no);
      if (idx >= 0) state.rows[idx] = r;
      patchSingleTableRow(r.no);
      break;
    }
    case "summary_updated":
      state.summary = evt.payload;
      renderSummaryAndOPD();
      break;
    case "workbook_reloaded":
      state.summary = evt.payload.summary;
      state.rows = evt.payload.rows || [];
      state.rowMap.clear();
      for (const r of state.rows) state.rowMap.set(r.no, r);
      populateFilterDropdowns();
      renderSummaryAndOPD();
      renderTable();
      break;
  }
}

/* Renderers */
function populateSettingsUI() {
  const s = state.settings;
  if (!s) return;
  if (document.activeElement !== el("inputBotPhone")) {
    el("inputBotPhone").value = s.botPhone || "";
  }
  if (document.activeElement !== el("selectQuestionMode")) {
    el("selectQuestionMode").value = s.questionMode || "natural";
  }

  el("cfgReadMin").value = s.readDelayMinMs;
  el("cfgReadMax").value = s.readDelayMaxMs;
  el("cfgTypingPerChar").value = s.typingMsPerChar;
  el("cfgTypingMin").value = s.typingMinMs;
  el("cfgTypingMax").value = s.typingMaxMs;
  el("cfgDelayMin").value = s.delayMinMs;
  el("cfgDelayMax").value = s.delayMaxMs;
  el("cfgCooldownEveryMin").value = s.cooldownEveryMin;
  el("cfgCooldownEveryMax").value = s.cooldownEveryMax;
  el("cfgCooldownMin").value = s.cooldownMinMs;
  el("cfgCooldownMax").value = s.cooldownMaxMs;
  el("cfgQuietWindow").value = s.quietWindowMs;
  el("cfgReplyTimeout").value = s.replyTimeoutMs;
}

function renderAIStatus() {
  const ai = state.ai;
  if (!ai) return;
  const badge = el("aiEngineBadge");
  if (ai.enabled) {
    badge.textContent = `LLM Aktif: ${ai.model}`;
    badge.title = ai.provider;
  } else {
    badge.textContent = "Smart Local Engine (100% Gratis / Offline)";
    badge.title = "Berjalan lokal tanpa biaya API eksternal";
  }
}

function renderWAStatus() {
  const wa = state.wa;
  if (!wa) return;
  const dot = el("waDot");
  const lbl = el("waLabel");
  const modalStatus = el("waModalStatusBox");
  const loginBox = el("waLoginControls");
  const logoutBox = el("waLogoutControls");
  const qrContainer = el("qrContainer");
  const qrImg = el("qrImage");
  const pairBox = el("pairingCodeBox");

  if (wa.connected && wa.loggedIn) {
    dot.className = "status-dot online";
    const who = wa.pushName ? `${wa.pushName} (${wa.accountJid})` : wa.accountJid;
    lbl.textContent = `WA Terhubung: ${who || "Aktif"}`;
    modalStatus.innerHTML = `<strong>Terhubung &amp; Siap Menguji</strong><br/><span class="muted">Akun: ${escapeHTML(who)}</span>`;
    loginBox.classList.add("hidden");
    logoutBox.classList.remove("hidden");
  } else if (wa.loggedIn && !wa.connected) {
    dot.className = "status-dot connecting";
    lbl.textContent = "WA Tersimpan (Klik untuk Sambung)";
    modalStatus.innerHTML = `<strong>Sesi Tersimpan (Belum Tersambung)</strong><br/><span class="muted">Klik tombol di bawah untuk menyambungkan ulang.</span>`;
    loginBox.classList.remove("hidden");
    logoutBox.classList.remove("hidden");
  } else {
    dot.className = "status-dot offline";
    lbl.textContent = "WA Penguji: Belum Terhubung";
    modalStatus.innerHTML = `<strong>Belum Tertaut</strong><br/><span class="muted">${escapeHTML(wa.lastError || "Gunakan nomor WA sekunder/tester melalui QR atau Kode Pairing.")}</span>`;
    loginBox.classList.remove("hidden");
    logoutBox.classList.add("hidden");
  }

  if (wa.qrDataUrl && !wa.connected) {
    qrImg.src = wa.qrDataUrl;
    qrContainer.classList.remove("hidden");
  } else {
    qrContainer.classList.add("hidden");
  }

  if (wa.pairingCode && !wa.connected) {
    pairBox.textContent = wa.pairingCode;
    pairBox.classList.remove("hidden");
  } else {
    pairBox.classList.add("hidden");
  }
}

function renderRunnerStatus() {
  const r = state.runner;
  if (!r) return;
  const isRunning = r.state === "running";
  const isPaused = r.state === "paused";

  const btnStart = el("btnStartBatch");
  const btnPause = el("btnPauseBatch");
  const btnStop = el("btnStopBatch");

  btnStart.disabled = isRunning;
  btnStart.textContent = isPaused ? "Lanjutkan Batch" : "Mulai / Lanjut Uji";
  btnPause.disabled = !isRunning;
  btnStop.disabled = !isRunning && !isPaused;

  const counter = el("tmRunnerCounter");
  if (isRunning) {
    counter.textContent = `Berjalan (${r.mode}): ${r.processedInRun}/${r.targetInRun} selesai • Aktif #${r.currentRowNo}`;
  } else if (isPaused) {
    counter.textContent = `Dijeda pada antrean (${r.processedInRun}/${r.targetInRun})`;
  } else {
    counter.textContent = "Antrean: Idle";
  }

  const alertEl = el("globalAlert");
  if (r.lastError) {
    alertEl.textContent = `Peringatan Runner: ${r.lastError}`;
    alertEl.classList.remove("hidden");
  } else {
    alertEl.classList.add("hidden");
  }
}

function renderTelemetry() {
  const t = state.telemetry;
  const s = state.settings;
  if (!t) return;

  const phaseBadge = el("humanPhaseBadge");
  phaseBadge.textContent = (t.phase || "idle").toUpperCase().replaceAll("_", " ");
  if (t.phase === "circuit_breaker") {
    phaseBadge.className = "badge badge-bad";
  } else if (t.phase === "micro_break") {
    phaseBadge.className = "badge badge-warn";
  } else if (t.phase === "idle" || t.phase === "paused") {
    phaseBadge.className = "badge badge-neutral";
  } else {
    phaseBadge.className = "badge badge-active";
  }

  el("humanPresenceBadge").textContent = `Presence: ${t.presence || "unavailable"}`;
  el("humanPhaseLabel").textContent = t.phaseLabel || "Menunggu perintah pengujian";

  el("tmBreakCounter").textContent = `${t.sinceLastBreak || 0} / ${t.nextBreakTarget || 18} soal`;
  el("tmBreakSub").textContent = `${t.totalBreaksTaken || 0}x micro-break selesai`;

  el("tmTypingInfo").textContent = `${t.lastTypingChars || 0} huruf (${((t.lastTypingDurationMs || 0) / 1000).toFixed(1)} dtk)`;
  if (s) {
    el("tmTypingRate").textContent = `~${s.typingMsPerChar} ms/huruf (${(s.typingMinMs / 1000).toFixed(1)}-${(s.typingMaxMs / 1000).toFixed(1)} dtk)`;
    el("tmReadRange").textContent = `Rentang ${(s.readDelayMinMs / 1000).toFixed(1)} - ${(s.readDelayMaxMs / 1000).toFixed(1)} dtk`;
    el("tmDelayRange").textContent = `Acak ${(s.delayMinMs / 1000).toFixed(1)} - ${(s.delayMaxMs / 1000).toFixed(1)} dtk`;
    el("tmQuietInfo").textContent = `${(s.quietWindowMs / 1000).toFixed(1)} dtk hening`;
    el("tmTimeoutInfo").textContent = `Batas tunggu ${(s.replyTimeoutMs / 1000).toFixed(0)} dtk`;
  }
  el("tmReadInfo").textContent = `${((t.lastReadDurationMs || 0) / 1000).toFixed(1)} dtk`;
  el("tmInterDelay").textContent = `${((t.lastInterDelayMs || 0) / 1000).toFixed(1)} dtk`;

  const circuitEl = el("tmCircuitState");
  if (t.circuitBreakerTripped) {
    circuitEl.textContent = "PAUSED (Circuit Breaker Aktif)";
    circuitEl.className = "text-danger";
  } else {
    circuitEl.textContent = "Aman (2-Arah Terkendali)";
    circuitEl.className = "text-ok";
  }

  const logs = t.recentLogs || [];
  el("logCountLabel").textContent = String(logs.length);
  el("behaviorLogList").innerHTML = logs
    .map((l) => {
      const dt = new Date(l.timestamp).toLocaleTimeString("id-ID", { hour12: false });
      return `<li><span class="log-time">[${escapeHTML(dt)}]</span><span class="log-phase">${escapeHTML(l.phase)}</span><span>${escapeHTML(l.detail)}</span></li>`;
    })
    .join("");

  updateLiveCountdownTick();
}

function updateLiveCountdownTick() {
  const t = state.telemetry;
  const cdEl = el("humanCountdown");
  const barEl = el("humanPhaseBar");
  if (!t || !t.phaseEndsAt || !t.phaseDurationMs || t.phase === "idle" || t.phase === "paused" || t.phase === "circuit_breaker") {
    cdEl.textContent = "0.0 dtk";
    barEl.style.width = "0%";
    return;
  }
  const now = Date.now();
  const remMs = Math.max(0, t.phaseEndsAt - now);
  const elapsed = Math.min(t.phaseDurationMs, Math.max(0, now - t.phaseStartedAt));
  const pct = Math.min(100, Math.max(0, (elapsed / t.phaseDurationMs) * 100));
  cdEl.textContent = `${(remMs / 1000).toFixed(1)} dtk`;
  barEl.style.width = `${pct.toFixed(1)}%`;
}

function renderSummaryAndOPD() {
  const sum = state.summary;
  if (!sum) return;

  el("excelFileNameLabel").textContent = sum.fileName || "Excel";
  el("tabCountTotal").textContent = String(sum.total);
  el("kpiTotal").textContent = String(sum.total);
  el("kpiBankSplit").textContent = `${sum.approved} approved • ${sum.waiting} waiting`;
  el("kpiTested").textContent = String(sum.tested);
  el("kpiProgressPct").textContent = `${sum.progressPct}% selesai`;
  el("kpiCocok").textContent = String(sum.cocok);
  el("kpiAccuracyPct").textContent = `Akurasi ${sum.accuracyPct}% dari teruji`;
  el("kpiTidak").textContent = String(sum.tidak);
  el("kpiBelum").textContent = String(sum.belumDiuji);

  const tbody = el("opdTableBody");
  const byOpd = sum.byOpd || [];
  tbody.innerHTML = byOpd
    .map(
      (item) => `
      <tr class="clickable-opd" data-opd="${escapeHTML(item.opd)}" title="Klik untuk memfilter OPD ini">
        <td><strong>${escapeHTML(item.opd)}</strong></td>
        <td class="num">${item.total}</td>
        <td class="num text-ok"><strong>${item.cocok}</strong></td>
        <td class="num text-danger"><strong>${item.tidak}</strong></td>
        <td class="num">${item.belumDiuji}</td>
        <td class="num">${item.cocok + item.tidak > 0 ? item.accuracyPct + "%" : "-"}</td>
      </tr>`
    )
    .join("");
}

function populateFilterDropdowns() {
  const sum = state.summary;
  if (!sum) return;

  const opdSel = el("filterOPD");
  const curOpd = state.filters.opd;
  opdSel.innerHTML =
    `<option value="">Semua OPD (${(sum.byOpd || []).length})</option>` +
    (sum.byOpd || [])
      .map((o) => `<option value="${escapeHTML(o.opd)}">${escapeHTML(o.opd)} (${o.total})</option>`)
      .join("");
  opdSel.value = curOpd;

  const katSel = el("filterKategori");
  const curKat = state.filters.kategori;
  katSel.innerHTML =
    `<option value="">Semua Kategori</option>` +
    (sum.categories || []).map((c) => `<option value="${escapeHTML(c)}">${escapeHTML(c)}</option>`).join("");
  katSel.value = curKat;
}

function rowMatchesFilter(r) {
  const f = state.filters;
  if (f.opd && r.opd !== f.opd) return false;
  if (f.kategori && r.kategori !== f.kategori) return false;
  if (f.statusBank && r.statusBank !== f.statusBank) return false;
  if (f.result === "UNSET" && r.resultStatus !== "") return false;
  if (f.result && f.result !== "UNSET" && r.resultStatus !== f.result) return false;

  if (f.search) {
    const q = f.search.toLowerCase();
    const hay = `${r.no} ${r.opd} ${r.kategori} ${r.questionRef} ${r.questionInput} ${r.expectedReply} ${r.actualReply} ${r.notes}`.toLowerCase();
    if (!hay.includes(q)) return false;
  }
  return true;
}

function buildRowInnerHTML(r) {
  const statusBankBadge =
    r.statusBank === "waiting"
      ? `<span class="badge badge-warn">waiting</span>`
      : `<span class="badge badge-ok">approved</span>`;

  let resBadge = `<span class="badge badge-neutral">Belum Diuji</span>`;
  if (r.resultStatus === "Cocok") {
    resBadge = `<span class="badge badge-ok">Cocok</span>`;
  } else if (r.resultStatus === "Tidak") {
    resBadge = `<span class="badge badge-bad">Tidak Cocok</span>`;
  }

  const qInputHtml = r.questionInput
    ? `<div class="q-input-text">${escapeHTML(r.questionInput)}</div>`
    : `<div class="q-input-text"><span class="reply-empty">Otomatis dibuat saat diuji (sesuai mode)</span></div>`;

  const noteHtml = r.notes ? `<div class="note-preview">${escapeHTML(r.notes)}</div>` : "";

  return `
    <td class="col-no">#${r.no}</td>
    <td class="col-meta">
      <div class="opd-name-cell">${escapeHTML(r.opd)}</div>
      <div class="row-btn-group">
        <span class="badge badge-outline">${escapeHTML(r.kategori)}</span>
        ${statusBankBadge}
      </div>
    </td>
    <td class="col-q">
      <div class="q-block-label">Acuan Bank:</div>
      <div class="q-ref-text">${escapeHTML(r.questionRef)}</div>
      <div class="q-block-label">Diinput ke Bot:</div>
      ${qInputHtml}
      <div class="row-btn-group">
        <button type="button" class="btn btn-outline btn-xs" data-action="gen-natural" data-no="${r.no}" title="Buat kalimat natural warga">Natural</button>
        <button type="button" class="btn btn-outline btn-xs" data-action="gen-chaos" data-no="${r.no}" title="Buat kalimat aneh/typo logis">Aneh/Error</button>
        <button type="button" class="btn btn-outline btn-xs" data-action="edit" data-no="${r.no}">Edit / Detail</button>
      </div>
    </td>
    <td class="col-a">${formatReplyHTML(r.expectedReply)}</td>
    <td class="col-a">${formatReplyHTML(r.actualReply)}</td>
    <td class="col-res">
      <div class="row-actions-stack">
        <div>${resBadge}</div>
        ${noteHtml}
        <div class="row-btn-group">
          <button type="button" class="btn btn-primary btn-xs" data-action="run" data-no="${r.no}">Uji ke WA</button>
          <button type="button" class="btn btn-outline btn-xs" data-action="set-cocok" data-no="${r.no}">Cocok</button>
          <button type="button" class="btn btn-outline btn-xs" data-action="set-tidak" data-no="${r.no}">Tidak</button>
        </div>
      </div>
    </td>
  `;
}

function renderTable() {
  const tbody = el("qaTableBody");
  const filtered = state.rows.filter(rowMatchesFilter);
  el("filteredCountInfo").textContent = `Menampilkan ${filtered.length} dari ${state.rows.length} baris`;

  if (filtered.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty-cell">Tidak ada baris pertanyaan yang cocok dengan filter saat ini.</td></tr>`;
    return;
  }

  const activeNo = state.runner?.state === "running" ? state.runner.currentRowNo : 0;
  tbody.innerHTML = filtered
    .map((r) => {
      const cls = r.no === activeNo ? ` class="row-running"` : "";
      return `<tr id="qa-row-${r.no}"${cls}>${buildRowInnerHTML(r)}</tr>`;
    })
    .join("");
}

function patchSingleTableRow(no) {
  const r = state.rowMap.get(no);
  if (!r) return;
  const existing = document.getElementById(`qa-row-${no}`);
  const matches = rowMatchesFilter(r);

  if (!matches && existing) {
    renderTable();
    return;
  }
  if (matches && !existing) {
    renderTable();
    return;
  }
  if (existing) {
    const activeNo = state.runner?.state === "running" ? state.runner.currentRowNo : 0;
    existing.className = r.no === activeNo ? "row-running" : "";
    existing.innerHTML = buildRowInnerHTML(r);
  }
}

/* Interactive Event Bindings */
function bindEvents() {
  // Tabs
  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      switchTab(btn.dataset.tab);
    });
  });

  // OPD click -> filter bank table
  el("opdTableBody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-opd]");
    if (!tr) return;
    state.filters.opd = tr.dataset.opd;
    el("filterOPD").value = state.filters.opd;
    switchTab("tab-bank");
    renderTable();
  });

  // Filters
  el("filterSearch").addEventListener("input", (e) => {
    state.filters.search = e.target.value.trim();
    renderTable();
  });
  el("filterOPD").addEventListener("change", (e) => {
    state.filters.opd = e.target.value;
    renderTable();
  });
  el("filterKategori").addEventListener("change", (e) => {
    state.filters.kategori = e.target.value;
    renderTable();
  });
  el("filterStatusBank").addEventListener("change", (e) => {
    state.filters.statusBank = e.target.value;
    renderTable();
  });
  el("filterResult").addEventListener("change", (e) => {
    state.filters.result = e.target.value;
    renderTable();
  });
  el("btnResetFilters").addEventListener("click", () => {
    state.filters = { search: "", opd: "", kategori: "", statusBank: "", result: "" };
    el("filterSearch").value = "";
    el("filterOPD").value = "";
    el("filterKategori").value = "";
    el("filterStatusBank").value = "";
    el("filterResult").value = "";
    renderTable();
  });

  // Save Quick Target Settings
  el("btnSaveQuickSettings").addEventListener("click", async () => {
    await saveSettingsFromInputs();
  });
  el("selectQuestionMode").addEventListener("change", async () => {
    await saveSettingsFromInputs();
  });

  // Granular Human Behavior Form & Presets
  document.querySelectorAll("[data-preset]").forEach((btn) => {
    btn.addEventListener("click", () => applyHumanPreset(btn.dataset.preset));
  });
  el("formHumanSettings").addEventListener("submit", async (e) => {
    e.preventDefault();
    await saveSettingsFromInputs();
  });

  // Runner Controls
  el("btnStartBatch").addEventListener("click", async () => {
    try {
      await saveSettingsFromInputs(true);
      await api("/api/runner/start", { method: "POST", body: { forceAll: false } });
      toast("Pengujian sekuensial dimulai.");
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnRerunAllConfirm").addEventListener("click", async () => {
    if (!confirm("Uji ulang seluruh 210 baris dari awal (termasuk baris yang sudah punya status)?")) return;
    try {
      await saveSettingsFromInputs(true);
      await api("/api/runner/start", { method: "POST", body: { forceAll: true } });
      toast("Pengujian ulang penuh 210 baris dimulai.");
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnPauseBatch").addEventListener("click", async () => {
    try {
      await api("/api/runner/pause", { method: "POST" });
      toast("Pengujian dijeda.");
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnStopBatch").addEventListener("click", async () => {
    try {
      await api("/api/runner/stop", { method: "POST" });
      toast("Pengujian dihentikan.");
    } catch (err) {
      toast(err.message, true);
    }
  });

  // Table Row Delegated Actions
  el("qaTableBody").addEventListener("click", async (e) => {
    const btn = e.target.closest("button[data-action]");
    if (!btn) return;
    const no = Number(btn.dataset.no);
    const action = btn.dataset.action;

    try {
      if (action === "run") {
        await saveSettingsFromInputs(true);
        await api(`/api/rows/${no}/run`, { method: "POST" });
        toast(`Menguji Baris #${no} ke WhatsApp...`);
      } else if (action === "gen-natural") {
        btn.disabled = true;
        await api(`/api/rows/${no}/generate`, { method: "POST", body: { mode: "natural", save: true } });
        toast(`Kalimat natural dibuat untuk Baris #${no}`);
      } else if (action === "gen-chaos") {
        btn.disabled = true;
        await api(`/api/rows/${no}/generate`, { method: "POST", body: { mode: "chaos", save: true } });
        toast(`Kalimat variasi user-error dibuat untuk Baris #${no}`);
      } else if (action === "set-cocok") {
        await api(`/api/rows/${no}`, { method: "PATCH", body: { resultStatus: "Cocok" } });
        toast(`Baris #${no} ditandai Cocok & tersimpan ke Excel`);
      } else if (action === "set-tidak") {
        await api(`/api/rows/${no}`, { method: "PATCH", body: { resultStatus: "Tidak" } });
        toast(`Baris #${no} ditandai Tidak Cocok & tersimpan ke Excel`);
      } else if (action === "edit") {
        openEditModal(no);
      }
    } catch (err) {
      toast(err.message, true);
    } finally {
      btn.disabled = false;
    }
  });

  // Upload Excel
  el("inputUploadExcel").addEventListener("change", async (e) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    const fd = new FormData();
    fd.append("file", file);
    try {
      await api("/api/excel/upload", { method: "POST", body: fd });
      toast(`Berhasil memuat berkas ${file.name}`);
    } catch (err) {
      toast(err.message, true);
    }
  });

  // WA Modal
  el("btnWaModal").addEventListener("click", () => el("waModal").classList.remove("hidden"));
  el("btnCloseWaModal").addEventListener("click", () => el("waModal").classList.add("hidden"));
  el("btnTriggerQR").addEventListener("click", async () => {
    try {
      await api("/api/wa/connect", { method: "POST" });
      toast("Menghubungkan sesi WhatsApp...");
    } catch (err) {
      toast(err.message, true);
    }
  });
  el("btnRequestPairing").addEventListener("click", async () => {
    const phone = el("inputPairingPhone").value.trim();
    if (!phone) {
      toast("Masukkan nomor HP penguji terlebih dahulu", true);
      return;
    }
    try {
      const res = await api("/api/wa/pair", { method: "POST", body: { phone } });
      toast(`Kode Pairing dibuat: ${res.pairingCode}`);
    } catch (err) {
      toast(err.message, true);
    }
  });
  el("btnLogoutWA").addEventListener("click", async () => {
    if (!confirm("Putuskan dan hapus sesi login WhatsApp penguji saat ini?")) return;
    try {
      await api("/api/wa/logout", { method: "POST" });
      toast("Sesi WhatsApp berhasil diputuskan.");
    } catch (err) {
      toast(err.message, true);
    }
  });

  // Edit Row Modal
  el("btnCloseEditModal").addEventListener("click", () => el("editModal").classList.add("hidden"));
  document.querySelectorAll("[data-modal-gen]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const no = Number(el("editRowNo").value);
      const mode = btn.dataset.modalGen;
      try {
        const res = await api(`/api/rows/${no}/generate`, { method: "POST", body: { mode, save: false } });
        el("editQuestionInput").value = res.generated;
      } catch (err) {
        toast(err.message, true);
      }
    });
  });

  el("btnReevalModal").addEventListener("click", async () => {
    const no = Number(el("editRowNo").value);
    try {
      // Save any edited actual reply first before reevaluating
      await api(`/api/rows/${no}`, {
        method: "PATCH",
        body: { actualReply: el("editActualReply").value },
      });
      const updated = await api(`/api/rows/${no}/evaluate`, { method: "POST" });
      el("editResultStatus").value = updated.resultStatus || "";
      el("editNotes").value = updated.notes || "";
      toast(`Baris #${no} dievaluasi ulang -> ${updated.resultStatus}`);
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("formEditRow").addEventListener("submit", async (e) => {
    e.preventDefault();
    const no = Number(el("editRowNo").value);
    try {
      await api(`/api/rows/${no}`, {
        method: "PATCH",
        body: {
          questionInput: el("editQuestionInput").value,
          actualReply: el("editActualReply").value,
          resultStatus: el("editResultStatus").value,
          notes: el("editNotes").value,
        },
      });
      el("editModal").classList.add("hidden");
      toast(`Perubahan Baris #${no} disimpan ke Excel.`);
    } catch (err) {
      toast(err.message, true);
    }
  });
}

function switchTab(tabId) {
  document.querySelectorAll(".tab-btn").forEach((b) => {
    b.classList.toggle("active", b.dataset.tab === tabId);
  });
  document.querySelectorAll(".tab-section").forEach((sec) => {
    sec.classList.toggle("active", sec.id === tabId);
  });
}

function openEditModal(no) {
  const r = state.rowMap.get(no);
  if (!r) return;
  el("editModalTitle").textContent = `Detail & Koreksi Baris #${r.no} — ${r.opd}`;
  el("editRowNo").value = String(r.no);
  el("editQuestionRef").textContent = r.questionRef;
  el("editQuestionInput").value = r.questionInput || "";
  el("editExpectedReply").textContent = r.expectedReply || "(Kosong / status waiting)";
  el("editActualReply").value = r.actualReply || "";
  el("editResultStatus").value = r.resultStatus || "";
  el("editNotes").value = r.notes || "";
  el("editModal").classList.remove("hidden");
}

function applyHumanPreset(preset) {
  if (preset === "standard") {
    el("cfgReadMin").value = 2500;
    el("cfgReadMax").value = 6000;
    el("cfgTypingPerChar").value = 55;
    el("cfgTypingMin").value = 2000;
    el("cfgTypingMax").value = 9000;
    el("cfgDelayMin").value = 8000;
    el("cfgDelayMax").value = 16000;
    el("cfgCooldownEveryMin").value = 15;
    el("cfgCooldownEveryMax").value = 25;
    el("cfgCooldownMin").value = 45000;
    el("cfgCooldownMax").value = 90000;
    el("cfgQuietWindow").value = 5000;
    el("cfgReplyTimeout").value = 30000;
  } else if (preset === "careful") {
    el("cfgReadMin").value = 4000;
    el("cfgReadMax").value = 8500;
    el("cfgTypingPerChar").value = 72;
    el("cfgTypingMin").value = 2800;
    el("cfgTypingMax").value = 11500;
    el("cfgDelayMin").value = 13000;
    el("cfgDelayMax").value = 24000;
    el("cfgCooldownEveryMin").value = 12;
    el("cfgCooldownEveryMax").value = 18;
    el("cfgCooldownMin").value = 65000;
    el("cfgCooldownMax").value = 120000;
    el("cfgQuietWindow").value = 6000;
    el("cfgReplyTimeout").value = 35000;
  } else if (preset === "brisk") {
    el("cfgReadMin").value = 1800;
    el("cfgReadMax").value = 4000;
    el("cfgTypingPerChar").value = 40;
    el("cfgTypingMin").value = 1500;
    el("cfgTypingMax").value = 6500;
    el("cfgDelayMin").value = 5500;
    el("cfgDelayMax").value = 10500;
    el("cfgCooldownEveryMin").value = 20;
    el("cfgCooldownEveryMax").value = 30;
    el("cfgCooldownMin").value = 30000;
    el("cfgCooldownMax").value = 55000;
    el("cfgQuietWindow").value = 4500;
    el("cfgReplyTimeout").value = 28000;
  }
  toast("Preset diterapkan ke form — klik Simpan untuk mengaktifkan.");
}

async function saveSettingsFromInputs(silent = false) {
  const payload = {
    botPhone: el("inputBotPhone").value.trim(),
    questionMode: el("selectQuestionMode").value,
    readDelayMinMs: Number(el("cfgReadMin").value),
    readDelayMaxMs: Number(el("cfgReadMax").value),
    typingMsPerChar: Number(el("cfgTypingPerChar").value),
    typingMinMs: Number(el("cfgTypingMin").value),
    typingMaxMs: Number(el("cfgTypingMax").value),
    delayMinMs: Number(el("cfgDelayMin").value),
    delayMaxMs: Number(el("cfgDelayMax").value),
    cooldownEveryMin: Number(el("cfgCooldownEveryMin").value),
    cooldownEveryMax: Number(el("cfgCooldownEveryMax").value),
    cooldownMinMs: Number(el("cfgCooldownMin").value),
    cooldownMaxMs: Number(el("cfgCooldownMax").value),
    quietWindowMs: Number(el("cfgQuietWindow").value),
    replyTimeoutMs: Number(el("cfgReplyTimeout").value),
  };
  const saved = await api("/api/settings", { method: "POST", body: payload });
  state.settings = saved;
  populateSettingsUI();
  if (!silent) {
    toast("Pengaturan target & perilaku manusia tersimpan.");
  }
}

bindEvents();
loadInitialState();
connectSSE();
setInterval(updateLiveCountdownTick, 120);
