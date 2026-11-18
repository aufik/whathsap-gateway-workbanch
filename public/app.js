const state = {
  settings: null,
  ai: null,
  wa: null,
  runner: null,
  telemetry: null,
  summary: null,
  rows: [],
  rowMap: new Map(),
  outOfBankSummary: null,
  outOfBankRows: [],
  oobMap: new Map(),
  filters: {
    search: "",
    opd: "",
    kategori: "",
    statusBank: "",
    result: "",
  },
  activeTab: "tab-bank",
  batchTargetSheet: "bank",
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

function formatReplyHTML(raw, mediaFiles) {
  let mediaHtml = "";
  if (Array.isArray(mediaFiles) && mediaFiles.length > 0) {
    mediaHtml = `
      <div class="media-gallery-grid">
        ${mediaFiles
          .map(
            (url, i) => `
          <div class="media-thumb-card" data-lightbox-src="${escapeHTML(url)}" title="Klik untuk perbesar gambar #${i + 1}">
            <img class="media-thumb-img" src="${escapeHTML(url)}" alt="Lampiran WA #${i + 1}" loading="lazy" />
            <div class="media-thumb-overlay">🔍 Perbesar</div>
          </div>`
          )
          .join("")}
      </div>
    `;
  }

  if (!raw || !raw.trim()) {
    if (mediaHtml) return mediaHtml;
    return `<span class="reply-empty">Belum ada balasan</span>`;
  }

  let safe = escapeHTML(raw);
  safe = safe.replace(/\[Berkas\]/gi, `<span class="tag-media">[Berkas]</span>`);
  safe = safe.replace(/\[Koordinat\]/gi, `<span class="tag-media tag-coord">[Koordinat]</span>`);
  return `<div class="reply-pre">${safe}</div>${mediaHtml}`;
}

function renderTimingCardHTML(m) {
  if (!m || (!m.typingMs && !m.testedAt && !m.unreadDelayMs)) {
    return `<div class="timing-badge-box"><span class="reply-empty">Belum diuji</span></div>`;
  }
  const bursts = m.typingBursts || 1;
  const breakLabel = m.breakKind === "long_rest" ? "Long-Rest" : m.breakKind === "micro_break" ? "Micro-Break" : m.breakKind === "spike" ? "Jeda Panjang" : "Jeda";
  return `
    <div class="timing-badge-box">
      <div class="timing-row">
        <span class="timing-label">Pre-Think:</span>
        <span class="timing-val">${((m.preThinkMs || 0) / 1000).toFixed(1)}s</span>
      </div>
      <div class="timing-row">
        <span class="timing-label">Ketik (${bursts}x):</span>
        <span class="timing-val">${((m.typingMs || 0) / 1000).toFixed(1)}s (${m.typingChars || 0}h)</span>
      </div>
      <div class="timing-row">
        <span class="timing-label">Tunggu Bot:</span>
        <span class="timing-val">${((m.botReplyWaitMs || 0) / 1000).toFixed(1)}s (${m.botBubbles || 0}b)</span>
      </div>
      <div class="timing-row">
        <span class="timing-label">Sblm Read (Abu):</span>
        <span class="timing-val highlight-unread">${((m.unreadDelayMs || 0) / 1000).toFixed(1)}s</span>
      </div>
      <div class="timing-row">
        <span class="timing-label">Baca (Biru):</span>
        <span class="timing-val">${((m.readDurationMs || 0) / 1000).toFixed(1)}s</span>
      </div>
      ${m.queueDelayMs ? `
      <div class="timing-row">
        <span class="timing-label">${escapeHTML(breakLabel)}:</span>
        <span class="timing-val">${((m.queueDelayMs || 0) / 1000).toFixed(1)}s</span>
      </div>` : ""}
    </div>
  `;
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
  for (const r of state.rows) state.rowMap.set(r.no, r);

  state.outOfBankSummary = snap.outOfBankSummary || { total: 0, cocok: 0, tidak: 0, belumDiuji: 0, progressPct: 0, safetyPct: 0 };
  state.outOfBankRows = snap.outOfBankRows || [];
  state.oobMap.clear();
  for (const r of state.outOfBankRows) state.oobMap.set(r.no, r);

  populateSettingsUI();
  populateFilterDropdowns();
  renderAIStatus();
  renderWAStatus();
  renderRunnerStatus();
  renderTelemetry();
  renderSummaryAndOPD();
  renderTable();
  renderOutOfBankSection();
}

function connectSSE() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    if (!e.data) return;
    try {
      const evt = JSON.parse(e.data);
      handleSSEEvent(evt);
    } catch {
      // ignore
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
      recalculateModalEstimates();
      break;
    case "runner_status": {
      const prevRow = state.runner?.currentRowNo;
      state.runner = evt.payload;
      renderRunnerStatus();
      if (prevRow && prevRow !== state.runner.currentRowNo) {
        patchSingleTableRow(prevRow);
        patchSingleOOBRow(prevRow);
      }
      if (state.runner.currentRowNo) {
        patchSingleTableRow(state.runner.currentRowNo);
        patchSingleOOBRow(state.runner.currentRowNo);
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
    case "oob_row_updated": {
      const r = evt.payload;
      state.oobMap.set(r.no, r);
      const idx = state.outOfBankRows.findIndex((x) => x.no === r.no);
      if (idx >= 0) state.outOfBankRows[idx] = r;
      patchSingleOOBRow(r.no);
      break;
    }
    case "oob_summary_updated":
      state.outOfBankSummary = evt.payload;
      renderOutOfBankSection();
      break;
    case "oob_reloaded":
      state.outOfBankRows = evt.payload.rows || [];
      state.outOfBankSummary = evt.payload.summary;
      state.oobMap.clear();
      for (const r of state.outOfBankRows) state.oobMap.set(r.no, r);
      renderOutOfBankSection();
      break;
    case "workbook_reloaded":
      state.summary = evt.payload.summary;
      state.rows = evt.payload.rows || [];
      state.rowMap.clear();
      for (const r of state.rows) state.rowMap.set(r.no, r);
      if (evt.payload.outOfBankRows) {
        state.outOfBankRows = evt.payload.outOfBankRows;
        state.outOfBankSummary = evt.payload.outOfBankSummary;
        state.oobMap.clear();
        for (const r of state.outOfBankRows) state.oobMap.set(r.no, r);
      }
      populateFilterDropdowns();
      renderSummaryAndOPD();
      renderTable();
      renderOutOfBankSection();
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

  el("cfgPreThinkMin").value = s.preThinkMinMs;
  el("cfgPreThinkMax").value = s.preThinkMaxMs;
  el("cfgTypingPerChar").value = s.typingMsPerChar;
  el("cfgTypingMin").value = s.typingMinMs;
  el("cfgTypingMax").value = s.typingMaxMs;
  el("cfgUnreadMin").value = s.unreadDelayMinMs;
  el("cfgUnreadMax").value = s.unreadDelayMaxMs;
  el("cfgReadMin").value = s.readDelayMinMs;
  el("cfgReadMax").value = s.readDelayMaxMs;
  el("cfgDelayMin").value = s.delayMinMs;
  el("cfgDelayMax").value = s.delayMaxMs;
  el("cfgDistractionSpikePct").value = s.distractionSpikePct;
  el("cfgDistractionMin").value = s.distractionMinMs;
  el("cfgDistractionMax").value = s.distractionMaxMs;
  el("cfgCooldownEveryMin").value = s.cooldownEveryMin;
  el("cfgCooldownEveryMax").value = s.cooldownEveryMax;
  el("cfgCooldownMin").value = s.cooldownMinMs;
  el("cfgCooldownMax").value = s.cooldownMaxMs;
  el("cfgLongRestEveryMin").value = s.longRestEveryMin;
  el("cfgLongRestEveryMax").value = s.longRestEveryMax;
  el("cfgLongRestMin").value = s.longRestMinMs;
  el("cfgLongRestMax").value = s.longRestMaxMs;
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
    badge.title = "Berjalan lokal di Go tanpa biaya API eksternal";
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
    lbl.textContent = "WA Tersimpan (Klik Sambung)";
    modalStatus.innerHTML = `<strong>Sesi Tersimpan (Belum Tersambung)</strong><br/><span class="muted">Klik tombol di bawah untuk menyambungkan.</span>`;
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

  const btnOpen = el("btnOpenBatchModal");
  const btnPause = el("btnPauseBatch");
  const btnStop = el("btnStopBatch");

  btnOpen.disabled = isRunning;
  btnOpen.textContent = isPaused ? "Lanjutkan Batch (Buka Modal)" : "Mulai / Lanjut Uji (Buka Modal)";
  btnPause.disabled = !isRunning;
  btnStop.disabled = !isRunning && !isPaused;

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
  } else if (t.phase === "micro_break" || t.phase === "long_rest") {
    phaseBadge.className = "badge badge-warn";
  } else if (t.phase === "idle" || t.phase === "paused") {
    phaseBadge.className = "badge badge-neutral";
  } else {
    phaseBadge.className = "badge badge-active";
  }

  el("humanPresenceBadge").textContent = `Presence: ${t.presence || "unavailable"}`;
  el("humanPhaseLabel").textContent = t.phaseLabel || "Menunggu perintah pengujian";

  el("tmPreThinkInfo").textContent = `${((t.lastPreThinkMs || 0) / 1000).toFixed(1)} dtk`;
  el("tmTypingInfo").textContent = `${t.lastTypingChars || 0} huruf (${((t.lastTypingDurationMs || 0) / 1000).toFixed(1)} dtk)`;
  el("tmTypingBursts").textContent = `${t.lastTypingBursts || 1} gelombang ketik`;

  el("tmUnreadInfo").textContent = `${((t.lastUnreadDelayMs || 0) / 1000).toFixed(1)} dtk`;
  el("tmReadInfo").textContent = `${((t.lastReadDurationMs || 0) / 1000).toFixed(1)} dtk`;
  el("tmInterDelay").textContent = `${((t.lastInterDelayMs || 0) / 1000).toFixed(1)} dtk`;

  el("tmBreakCounter").textContent = `${t.sinceLastBreak || 0} / ${t.nextBreakTarget || 15} soal (${t.totalBreaksTaken || 0}x rehat)`;
  el("tmLongRestCounter").textContent = `Long-Rest: ${t.sinceLastLongRest || 0} / ${t.nextLongRestTarget || 45} soal (${t.totalLongRestsTaken || 0}x rest)`;

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

function renderOutOfBankSection() {
  const sum = state.outOfBankSummary || { total: 0, cocok: 0, tidak: 0, belumDiuji: 0, progressPct: 0, safetyPct: 0 };
  el("tabCountOOB").textContent = String(sum.total || 0);
  el("kpiOOBTotal").textContent = String(sum.total || 0);
  el("kpiOOBTested").textContent = String(sum.tested || 0);
  el("kpiOOBProgressPct").textContent = `${sum.progressPct || 0}% selesai`;
  el("kpiOOBCocok").textContent = String(sum.cocok || 0);
  el("kpiOOBSafetyPct").textContent = `Aman dari halusinasi: ${sum.safetyPct || 0}%`;
  el("kpiOOBTidak").textContent = String(sum.tidak || 0);
  el("kpiOOBBelum").textContent = String(sum.belumDiuji || 0);

  const tbody = el("oobTableBody");
  if (!state.outOfBankRows || state.outOfBankRows.length === 0) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Belum ada baris uji diluar bank. Klik tombol "+ Generate Variasi Tahun 2026 dari Bank" di atas untuk membuat otomatis.</td></tr>`;
    return;
  }

  const activeNo = state.runner?.state === "running" && state.runner.targetSheet === "out_of_bank" ? state.runner.currentRowNo : 0;
  tbody.innerHTML = state.outOfBankRows
    .map((r) => {
      const cls = r.no === activeNo ? ` class="row-running"` : "";
      return `<tr id="oob-row-${r.no}"${cls}>${buildOOBRowInnerHTML(r)}</tr>`;
    })
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
    <td class="col-a">${formatReplyHTML(r.expectedReply, null)}</td>
    <td class="col-a">${formatReplyHTML(r.actualReply, r.mediaFiles)}</td>
    <td class="col-timing">${renderTimingCardHTML(r.metrics)}</td>
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

function buildOOBRowInnerHTML(r) {
  let resBadge = `<span class="badge badge-neutral">Belum Diuji</span>`;
  if (r.resultStatus === "Cocok") {
    resBadge = `<span class="badge badge-ok">Aman (Tidak Halusinasi)</span>`;
  } else if (r.resultStatus === "Tidak") {
    resBadge = `<span class="badge badge-bad">Halusinasi / Salah</span>`;
  }

  const noteHtml = r.notes ? `<div class="note-preview">${escapeHTML(r.notes)}</div>` : "";

  return `
    <td class="col-no">#${r.no}</td>
    <td class="col-meta">
      <div class="opd-name-cell">${escapeHTML(r.sourceRef)}</div>
      <span class="badge badge-outline">${escapeHTML(r.scenarioType)}</span>
      ${r.forbiddenYear ? `<div style="margin-top:4px"><span class="badge badge-warn">Tahun Uji: ${escapeHTML(r.forbiddenYear)}</span></div>` : ""}
    </td>
    <td class="col-q">
      <div class="q-block-label">Acuan di Bank:</div>
      <div class="q-ref-text" style="font-size:12px">${escapeHTML(r.baseQuestion)}</div>
      <div class="q-block-label">Diinput ke Bot (Diluar Bank):</div>
      <div class="q-input-text"><strong>${escapeHTML(r.probeQuestion)}</strong></div>
      <div class="row-btn-group">
        <button type="button" class="btn btn-outline btn-xs" data-oob-action="edit" data-no="${r.no}">Edit</button>
        <button type="button" class="btn btn-danger-outline btn-xs" data-oob-action="del" data-no="${r.no}">Hapus</button>
      </div>
    </td>
    <td class="col-a">
      <div style="font-size:12px; color:var(--text-muted)">${escapeHTML(r.expectedBehavior)}</div>
    </td>
    <td class="col-a">${formatReplyHTML(r.actualReply, r.mediaFiles)}</td>
    <td class="col-timing">${renderTimingCardHTML(r.metrics)}</td>
    <td class="col-res">
      <div class="row-actions-stack">
        <div>${resBadge}</div>
        ${noteHtml}
        <div class="row-btn-group">
          <button type="button" class="btn btn-primary btn-xs" data-oob-action="run" data-no="${r.no}">Uji ke WA</button>
          <button type="button" class="btn btn-outline btn-xs" data-oob-action="set-cocok" data-no="${r.no}">Cocok</button>
          <button type="button" class="btn btn-outline btn-xs" data-oob-action="set-tidak" data-no="${r.no}">Tidak</button>
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
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Tidak ada baris pertanyaan yang cocok dengan filter saat ini.</td></tr>`;
    return;
  }

  const activeNo = state.runner?.state === "running" && state.runner.targetSheet !== "out_of_bank" ? state.runner.currentRowNo : 0;
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
    const activeNo = state.runner?.state === "running" && state.runner.targetSheet !== "out_of_bank" ? state.runner.currentRowNo : 0;
    existing.className = r.no === activeNo ? "row-running" : "";
    existing.innerHTML = buildRowInnerHTML(r);
  }
}

function patchSingleOOBRow(no) {
  const r = state.oobMap.get(no);
  if (!r) return;
  const existing = document.getElementById(`oob-row-${no}`);
  if (existing) {
    const activeNo = state.runner?.state === "running" && state.runner.targetSheet === "out_of_bank" ? state.runner.currentRowNo : 0;
    existing.className = r.no === activeNo ? "row-running" : "";
    existing.innerHTML = buildOOBRowInnerHTML(r);
  }
}

/* Modal Launch Batch & Real-Time Time Estimator */
function openBatchLaunchModal(targetSheet = "bank") {
  state.batchTargetSheet = targetSheet;
  const isBank = targetSheet === "bank";
  const rows = isBank ? state.rows : state.outOfBankRows;
  const pendingCount = rows.filter((r) => !r.resultStatus).length;
  const totalCount = rows.length;

  el("batchModalTitle").textContent = isBank
    ? "Luncurkan Sesi Uji Bank Pertanyaan (Aman & Anti-Spam)"
    : "Luncurkan Sesi Uji Diluar Bank (Tahun 2026+)";

  el("batchRecTotal").textContent = String(pendingCount > 0 ? pendingCount : totalCount);
  el("batchMaxLabel").textContent = `dari ${pendingCount > 0 ? pendingCount : totalCount} soal (${pendingCount} belum diuji)`;

  const input = el("inputBatchCount");
  input.max = String(totalCount || 1);
  const defVal = pendingCount > 0 ? Math.min(30, pendingCount) : Math.min(30, totalCount);
  input.value = String(defVal || 1);
  el("checkForceAllBatch").checked = pendingCount === 0;

  recalculateModalEstimates();
  el("batchModal").classList.remove("hidden");
}

function recalculateModalEstimates() {
  const s = state.settings || {
    preThinkMinMs: 2000, preThinkMaxMs: 5000,
    typingMsPerChar: 105, typingMinMs: 4500, typingMaxMs: 20000,
    unreadDelayMinMs: 2500, unreadDelayMaxMs: 8000,
    readDelayMinMs: 4500, readDelayMaxMs: 13000,
    delayMinMs: 15000, delayMaxMs: 30000,
    distractionSpikePct: 22, distractionMinMs: 32000, distractionMaxMs: 60000,
    cooldownEveryMin: 12, cooldownEveryMax: 18,
    cooldownMinMs: 90000, cooldownMaxMs: 165000,
    longRestEveryMin: 40, longRestEveryMax: 50,
    longRestMinMs: 240000, longRestMaxMs: 420000,
    quietWindowMs: 5500, replyTimeoutMs: 35000,
  };

  const count = Math.max(1, Number(el("inputBatchCount").value) || 1);

  // Fast profile (assuming snappy 45-char questions, 1-bubble bot reply ~2.5s, min reading & delay bounds)
  const estFastPerQuestionMs =
    s.preThinkMinMs +
    Math.max(s.typingMinMs, 45 * s.typingMsPerChar * 0.78) +
    2500 + s.quietWindowMs +
    s.unreadDelayMinMs +
    s.readDelayMinMs +
    s.delayMinMs;

  // Safe profile (assuming rich 75-char questions, multi-bubble bot wait ~5.5s, max reading, distraction spikes)
  const spikeFactor = (s.distractionSpikePct || 22) / 100;
  const normalJitterMs = s.delayMaxMs * (1 - spikeFactor) + s.distractionMaxMs * spikeFactor;
  const estSafePerQuestionMs =
    s.preThinkMaxMs +
    Math.min(s.typingMaxMs, Math.max(s.typingMinMs, 75 * s.typingMsPerChar * 1.25) + 1200) +
    5500 + s.quietWindowMs +
    s.unreadDelayMaxMs +
    s.readDelayMaxMs +
    normalJitterMs;

  const avgMicroInterval = Math.max(5, (s.cooldownEveryMin + s.cooldownEveryMax) / 2);
  const microBreaks = Math.floor((count - 1) / avgMicroInterval);
  const fastMicroTimeMs = microBreaks * s.cooldownMinMs;
  const safeMicroTimeMs = microBreaks * s.cooldownMaxMs;

  const avgLongInterval = Math.max(15, (s.longRestEveryMin + s.longRestEveryMax) / 2);
  const longRests = Math.floor((count - 1) / avgLongInterval);
  const fastLongTimeMs = longRests * s.longRestMinMs;
  const safeLongTimeMs = longRests * s.longRestMaxMs;

  const totalFastMs = (count * estFastPerQuestionMs) + fastMicroTimeMs + fastLongTimeMs;
  const totalSafeMs = (count * estSafePerQuestionMs) + safeMicroTimeMs + safeLongTimeMs;

  const fastMin = Math.round(totalFastMs / 60000);
  const safeMin = Math.round(totalSafeMs / 60000);

  const now = Date.now();
  const fastETA = new Date(now + totalFastMs).toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit" });
  const safeETA = new Date(now + totalSafeMs).toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit" });

  el("estFastestDuration").textContent = fastMin >= 60 ? `${(fastMin / 60).toFixed(1)} jam (${fastMin} mnt)` : `${fastMin} menit`;
  el("estFastestETA").textContent = `Selesai sekitar jam ${fastETA}`;

  el("estSafestDuration").textContent = safeMin >= 60 ? `${(safeMin / 60).toFixed(1)} jam (${safeMin} mnt)` : `${safeMin} menit`;
  el("estSafestETA").textContent = `Selesai sekitar jam ${safeETA}`;

  el("estBreakPlan").textContent = `Jadwal istirahat: ${microBreaks}x Micro-Break (${((s.cooldownMinMs + s.cooldownMaxMs) / 120000).toFixed(1)} mnt) • ${longRests}x Long-Rest (${((s.longRestMinMs + s.longRestMaxMs) / 120000).toFixed(1)} mnt)`;
}

/* Event Bindings */
function bindEvents() {
  // Tabs
  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => switchTab(btn.dataset.tab));
  });

  // OPD click in summary table
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

  // Batch Launch Modal Triggers
  el("btnOpenBatchModal").addEventListener("click", () => openBatchLaunchModal("bank"));
  el("btnLaunchBankModal").addEventListener("click", () => openBatchLaunchModal("bank"));
  el("btnLaunchOOBBatchModal").addEventListener("click", () => openBatchLaunchModal("out_of_bank"));
  el("btnCloseBatchModal").addEventListener("click", () => el("batchModal").classList.add("hidden"));
  el("btnCancelBatchModal").addEventListener("click", () => el("batchModal").classList.add("hidden"));

  el("inputBatchCount").addEventListener("input", recalculateModalEstimates);
  document.querySelectorAll("[data-batch-preset]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const preset = btn.dataset.batchPreset;
      const isBank = state.batchTargetSheet === "bank";
      const rows = isBank ? state.rows : state.outOfBankRows;
      const pendingCount = rows.filter((r) => !r.resultStatus).length || rows.length;
      if (preset === "ALL") {
        el("inputBatchCount").value = String(pendingCount);
      } else {
        el("inputBatchCount").value = String(Math.min(pendingCount, Number(preset)));
      }
      recalculateModalEstimates();
    });
  });

  el("btnConfirmStartBatch").addEventListener("click", async () => {
    const limit = Number(el("inputBatchCount").value) || 30;
    const forceAll = el("checkForceAllBatch").checked;
    const targetSheet = state.batchTargetSheet || "bank";
    try {
      await saveSettingsFromInputs(true);
      await api("/api/runner/start", {
        method: "POST",
        body: { targetSheet, forceAll, limit },
      });
      el("batchModal").classList.add("hidden");
      toast(`Memulai pengujian batch ${targetSheet === "bank" ? "Bank Pertanyaan" : "Uji Diluar Bank"} (${limit} soal)...`);
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

  // Settings Save & Presets
  el("btnSaveQuickSettings").addEventListener("click", () => saveSettingsFromInputs());
  el("selectQuestionMode").addEventListener("change", () => saveSettingsFromInputs());
  el("formHumanSettings").addEventListener("submit", (e) => {
    e.preventDefault();
    saveSettingsFromInputs();
  });
  document.querySelectorAll("[data-preset]").forEach((btn) => {
    btn.addEventListener("click", () => applyHumanPreset(btn.dataset.preset));
  });

  // Bank Pertanyaan Row Actions
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

  // Out-of-Bank Sheet Actions
  el("btnAutoGenerateOOB").addEventListener("click", async () => {
    const year = prompt("Masukkan tahun di luar data bank yang ingin diuji ke bot:", "2026") || "2026";
    try {
      const res = await api("/api/oob/generate", {
        method: "POST",
        body: { count: 25, targetYear: year.trim() },
      });
      toast(`Berhasil membangkitkan ${res.rows?.length || 0} variasi soal Tahun ${year} pada sheet "Uji Diluar Bank"`);
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnOpenNewOOBModal").addEventListener("click", () => openOOBModal(0));
  el("btnCloseOOBModal").addEventListener("click", () => el("oobModal").classList.add("hidden"));

  el("oobTableBody").addEventListener("click", async (e) => {
    const btn = e.target.closest("button[data-oob-action]");
    if (!btn) return;
    const no = Number(btn.dataset.no);
    const action = btn.dataset.oobAction;

    try {
      if (action === "run") {
        await saveSettingsFromInputs(true);
        await api(`/api/oob/rows/${no}/run`, { method: "POST" });
        toast(`Menguji Soal Diluar Bank #${no} ke WhatsApp...`);
      } else if (action === "edit") {
        openOOBModal(no);
      } else if (action === "del") {
        if (!confirm(`Hapus soal Uji Diluar Bank #${no}?`)) return;
        await api(`/api/oob/rows/${no}`, { method: "DELETE" });
        toast(`Soal #${no} dihapus.`);
      } else if (action === "set-cocok") {
        await api(`/api/oob/rows/${no}`, { method: "PATCH", body: { resultStatus: "Cocok" } });
        toast(`Soal Diluar Bank #${no} ditandai Cocok (Aman) & tersimpan.`);
      } else if (action === "set-tidak") {
        await api(`/api/oob/rows/${no}`, { method: "PATCH", body: { resultStatus: "Tidak" } });
        toast(`Soal Diluar Bank #${no} ditandai Tidak (Halusinasi).`);
      }
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

  // Edit Modal (Bank Pertanyaan)
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

  // OOB Modal Form Save & Evaluate
  el("btnReevalOOBModal").addEventListener("click", async () => {
    const no = Number(el("oobRowNo").value);
    if (!no) return;
    try {
      await api(`/api/oob/rows/${no}`, {
        method: "PATCH",
        body: { actualReply: el("oobActualReply").value },
      });
      const updated = await api(`/api/oob/rows/${no}/evaluate`, { method: "POST" });
      el("oobResultStatus").value = updated.resultStatus || "";
      el("oobNotes").value = updated.notes || "";
      toast(`Evaluasi anti-halusinasi selesai -> ${updated.resultStatus}`);
    } catch (err) {
      toast(err.message, true);
    }
  });
  el("formOOBRow").addEventListener("submit", async (e) => {
    e.preventDefault();
    const no = Number(el("oobRowNo").value);
    const payload = {
      sourceRef: el("oobSourceRef").value.trim(),
      scenarioType: el("oobScenarioType").value.trim(),
      baseQuestion: el("oobBaseQuestion").value.trim(),
      probeQuestion: el("oobProbeQuestion").value.trim(),
      expectedBehavior: el("oobExpectedBehavior").value.trim(),
      actualReply: el("oobActualReply").value.trim(),
      resultStatus: el("oobResultStatus").value,
      notes: el("oobNotes").value.trim(),
    };
    try {
      if (no > 0) {
        await api(`/api/oob/rows/${no}`, { method: "PATCH", body: payload });
        toast(`Soal Diluar Bank #${no} diperbarui.`);
      } else {
        await api("/api/oob/rows", { method: "POST", body: payload });
        toast("Soal baru ditambahkan ke sheet Uji Diluar Bank.");
      }
      el("oobModal").classList.add("hidden");
    } catch (err) {
      toast(err.message, true);
    }
  });

  // Lightbox Preview Events
  document.addEventListener("click", (e) => {
    const card = e.target.closest("[data-lightbox-src]");
    if (card) {
      const src = card.dataset.lightboxSrc;
      el("lightboxImage").src = src;
      el("lightboxDownloadBtn").href = src;
      el("lightboxModal").classList.remove("hidden");
    }
  });

  el("btnCloseLightbox").addEventListener("click", () => {
    el("lightboxModal").classList.add("hidden");
  });
  el("lightboxModal").addEventListener("click", (e) => {
    if (e.target === el("lightboxModal")) {
      el("lightboxModal").classList.add("hidden");
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !el("lightboxModal").classList.contains("hidden")) {
      el("lightboxModal").classList.add("hidden");
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
}

function switchTab(tabId) {
  state.activeTab = tabId;
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

function openOOBModal(no) {
  el("oobRowNo").value = String(no);
  if (no > 0) {
    const r = state.oobMap.get(no);
    if (!r) return;
    el("oobModalTitle").textContent = `Edit Soal Uji Diluar Bank #${r.no}`;
    el("oobSourceRef").value = r.sourceRef || "";
    el("oobScenarioType").value = r.scenarioType || "";
    el("oobBaseQuestion").value = r.baseQuestion || "";
    el("oobProbeQuestion").value = r.probeQuestion || "";
    el("oobExpectedBehavior").value = r.expectedBehavior || "";
    el("oobActualReply").value = r.actualReply || "";
    el("oobResultStatus").value = r.resultStatus || "";
    el("oobNotes").value = r.notes || "";
  } else {
    el("oobModalTitle").textContent = "Tambah Soal Uji Diluar Bank Baru";
    el("oobSourceRef").value = "Input Manual Penguji";
    el("oobScenarioType").value = "Tahun Diluar Data (2026)";
    el("oobBaseQuestion").value = "";
    el("oobProbeQuestion").value = "";
    el("oobExpectedBehavior").value = "Bot wajib jujur menyatakan data Tahun 2026 belum tersedia dan tidak boleh berhalusinasi.";
    el("oobActualReply").value = "";
    el("oobResultStatus").value = "";
    el("oobNotes").value = "";
  }
  el("oobModal").classList.remove("hidden");
}

function applyHumanPreset(preset) {
  if (preset === "standard") {
    el("cfgPreThinkMin").value = 2000;
    el("cfgPreThinkMax").value = 5000;
    el("cfgTypingPerChar").value = 105;
    el("cfgTypingMin").value = 4500;
    el("cfgTypingMax").value = 20000;
    el("cfgUnreadMin").value = 2500;
    el("cfgUnreadMax").value = 8000;
    el("cfgReadMin").value = 4500;
    el("cfgReadMax").value = 13000;
    el("cfgDelayMin").value = 15000;
    el("cfgDelayMax").value = 30000;
    el("cfgDistractionSpikePct").value = 22;
    el("cfgDistractionMin").value = 32000;
    el("cfgDistractionMax").value = 60000;
    el("cfgCooldownEveryMin").value = 12;
    el("cfgCooldownEveryMax").value = 18;
    el("cfgCooldownMin").value = 90000;
    el("cfgCooldownMax").value = 165000;
    el("cfgLongRestEveryMin").value = 40;
    el("cfgLongRestEveryMax").value = 50;
    el("cfgLongRestMin").value = 240000;
    el("cfgLongRestMax").value = 420000;
    el("cfgQuietWindow").value = 5500;
    el("cfgReplyTimeout").value = 35000;
  } else if (preset === "careful") {
    el("cfgPreThinkMin").value = 3000;
    el("cfgPreThinkMax").value = 7000;
    el("cfgTypingPerChar").value = 140;
    el("cfgTypingMin").value = 6000;
    el("cfgTypingMax").value = 25000;
    el("cfgUnreadMin").value = 4000;
    el("cfgUnreadMax").value = 12000;
    el("cfgReadMin").value = 6000;
    el("cfgReadMax").value = 18000;
    el("cfgDelayMin").value = 22000;
    el("cfgDelayMax").value = 42000;
    el("cfgDistractionSpikePct").value = 30;
    el("cfgDistractionMin").value = 45000;
    el("cfgDistractionMax").value = 85000;
    el("cfgCooldownEveryMin").value = 10;
    el("cfgCooldownEveryMax").value = 15;
    el("cfgCooldownMin").value = 120000;
    el("cfgCooldownMax").value = 210000;
    el("cfgLongRestEveryMin").value = 30;
    el("cfgLongRestEveryMax").value = 40;
    el("cfgLongRestMin").value = 300000;
    el("cfgLongRestMax").value = 600000;
    el("cfgQuietWindow").value = 6000;
    el("cfgReplyTimeout").value = 40000;
  } else if (preset === "brisk") {
    el("cfgPreThinkMin").value = 1200;
    el("cfgPreThinkMax").value = 3000;
    el("cfgTypingPerChar").value = 75;
    el("cfgTypingMin").value = 3000;
    el("cfgTypingMax").value = 12000;
    el("cfgUnreadMin").value = 1500;
    el("cfgUnreadMax").value = 5000;
    el("cfgReadMin").value = 3000;
    el("cfgReadMax").value = 8500;
    el("cfgDelayMin").value = 10000;
    el("cfgDelayMax").value = 20000;
    el("cfgDistractionSpikePct").value = 15;
    el("cfgDistractionMin").value = 22000;
    el("cfgDistractionMax").value = 40000;
    el("cfgCooldownEveryMin").value = 15;
    el("cfgCooldownEveryMax").value = 22;
    el("cfgCooldownMin").value = 60000;
    el("cfgCooldownMax").value = 110000;
    el("cfgLongRestEveryMin").value = 45;
    el("cfgLongRestEveryMax").value = 60;
    el("cfgLongRestMin").value = 180000;
    el("cfgLongRestMax").value = 300000;
    el("cfgQuietWindow").value = 4500;
    el("cfgReplyTimeout").value = 30000;
  }
  toast("Preset diterapkan ke form — klik 'Simpan Seluruh Pengaturan' untuk mengaktifkan.");
}

async function saveSettingsFromInputs(silent = false) {
  const payload = {
    botPhone: el("inputBotPhone").value.trim(),
    questionMode: el("selectQuestionMode").value,
    preThinkMinMs: Number(el("cfgPreThinkMin").value),
    preThinkMaxMs: Number(el("cfgPreThinkMax").value),
    typingMsPerChar: Number(el("cfgTypingPerChar").value),
    typingMinMs: Number(el("cfgTypingMin").value),
    typingMaxMs: Number(el("cfgTypingMax").value),
    unreadDelayMinMs: Number(el("cfgUnreadMin").value),
    unreadDelayMaxMs: Number(el("cfgUnreadMax").value),
    readDelayMinMs: Number(el("cfgReadMin").value),
    readDelayMaxMs: Number(el("cfgReadMax").value),
    delayMinMs: Number(el("cfgDelayMin").value),
    delayMaxMs: Number(el("cfgDelayMax").value),
    distractionSpikePct: Number(el("cfgDistractionSpikePct").value),
    distractionMinMs: Number(el("cfgDistractionMin").value),
    distractionMaxMs: Number(el("cfgDistractionMax").value),
    cooldownEveryMin: Number(el("cfgCooldownEveryMin").value),
    cooldownEveryMax: Number(el("cfgCooldownEveryMax").value),
    cooldownMinMs: Number(el("cfgCooldownMin").value),
    cooldownMaxMs: Number(el("cfgCooldownMax").value),
    longRestEveryMin: Number(el("cfgLongRestEveryMin").value),
    longRestEveryMax: Number(el("cfgLongRestEveryMax").value),
    longRestMinMs: Number(el("cfgLongRestMin").value),
    longRestMaxMs: Number(el("cfgLongRestMax").value),
    quietWindowMs: Number(el("cfgQuietWindow").value),
    replyTimeoutMs: Number(el("cfgReplyTimeout").value),
  };
  const saved = await api("/api/settings", { method: "POST", body: payload });
  state.settings = saved;
  populateSettingsUI();
  recalculateModalEstimates();
  if (!silent) {
    toast("Pengaturan target & ritme manusia tersimpan.");
  }
}

bindEvents();
loadInitialState();
connectSSE();
setInterval(updateLiveCountdownTick, 120);
