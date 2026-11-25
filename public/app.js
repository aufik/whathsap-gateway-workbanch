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
  oobSelectedSet: new Set(),
  stressTestSummary: null,
  stressTestRows: [],
  stressMap: new Map(),
  stressSelectedSet: new Set(),
  filters: {
    search: "",
    opd: "",
    kategori: "",
    statusBank: "",
    result: "",
  },
  activeTab: "tab-bank",
  batchTargetSheet: "bank",
  currentUser: null,
  devices: [],
  deviceStatuses: {},
  channelTelemetry: {},
  selectedDeviceUuid: "",
};

const el = (id) => document.getElementById(id);
const BASE_PATH = window.location.pathname.replace(/\/(?:index\.html)?$/, "").replace(/\/$/, "");

async function api(path, options = {}) {
  const opts = { ...options, headers: { ...(options.headers || {}) } };
  if (opts.body && !(opts.body instanceof FormData) && typeof opts.body === "object") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const fullPath = (BASE_PATH && path.startsWith("/") && !path.startsWith(BASE_PATH + "/"))
    ? (BASE_PATH + path)
    : path;
  const res = await fetch(fullPath, opts);
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

function renderSenderPhoneHTML(m, rowNo) {
  // If row is actively running, show which device channel is processing it
  const activeList = devicesForRow(rowNo);
  if (activeList.length > 0) {
    const dev = activeList[0];
    return `<span class="device-phone-badge" style="color:var(--primary); font-weight:700" title="${escapeHTML(dev.phone)}">▶ ${escapeHTML(dev.phone || dev.label)}</span>`;
  }
  // Otherwise show the phone that completed the test
  const phone = m?.senderPhone || m?.testedByDevice?.phoneNumber || "";
  if (!phone) {
    return `<span class="reply-empty"></span>`; // left completely blank as requested
  }
  // Clean JID suffix if present (@s.whatsapp.net)
  const clean = phone.replace(/@.*$/, "");
  return `<span class="device-phone-badge">${escapeHTML(clean)}</span>`;
}

function renderTargetPhoneHTML(m) {
  const phone = m?.botTargetPhone || "";
  if (!phone) {
    return `<span class="reply-empty"></span>`; // left completely blank as requested
  }
  const clean = phone.replace(/@.*$/, "");
  return `<span class="device-phone-badge">${escapeHTML(clean)}</span>`;
}

function renderChatTimeHTML(testedAt) {
  if (!testedAt) {
    return `<span class="reply-empty">-</span>`;
  }
  const d = new Date(testedAt);
  if (isNaN(d.getTime())) {
    return `<span class="reply-empty">-</span>`;
  }
  const pad = (n) => String(n).padStart(2, "0");
  const dateStr = `${pad(d.getDate())}/${pad(d.getMonth() + 1)}/${d.getFullYear()}`;
  const timeStr = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  return `<div class="chat-time-box"><span class="chat-time-date">${dateStr}</span><span class="chat-time-hour">${timeStr}</span></div>`;
}

function renderDeviceDualStackHTML(m, rowNo) {
  const sender = renderSenderPhoneHTML(m, rowNo);
  const target = renderTargetPhoneHTML(m);
  return `
    <div class="device-dual-stack">
      <div class="device-row"><span class="device-tag">Dari:</span>${sender}</div>
      <div class="device-row"><span class="device-tag">Ke:</span>${target}</div>
    </div>
  `;
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

  state.stressTestSummary = snap.stressTestSummary || { total: 0, cocok: 0, tidak: 0, belumDiuji: 0, progressPct: 0, resiliencePct: 0 };
  state.stressTestRows = snap.stressTestRows || [];
  state.stressMap.clear();
  for (const r of state.stressTestRows) state.stressMap.set(r.no, r);

  state.channelTelemetry = snap.channelTelemetry || {};

  const safeCall = (fn, name) => {
    try { fn(); } catch (e) { console.warn(`Render error in ${name}:`, e); }
  };
  safeCall(populateSettingsUI, "populateSettingsUI");
  safeCall(populateFilterDropdowns, "populateFilterDropdowns");
  safeCall(renderAIStatus, "renderAIStatus");
  safeCall(renderWAStatus, "renderWAStatus");
  safeCall(renderRunnerStatus, "renderRunnerStatus");
  safeCall(renderTelemetry, "renderTelemetry");
  safeCall(renderSummaryAndOPD, "renderSummaryAndOPD");
  safeCall(renderTable, "renderTable");
  safeCall(renderOutOfBankSection, "renderOutOfBankSection");
  safeCall(renderStressSection, "renderStressSection");
}

function connectSSE() {
  const sseUrl = (BASE_PATH && !"/api/events".startsWith(BASE_PATH)) ? (BASE_PATH + "/api/events") : "/api/events";
  const es = new EventSource(sseUrl);
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
    case "wa_status": {
      const p = evt.payload || {};
      if (p.deviceUuid) {
        state.deviceStatuses = { ...(state.deviceStatuses || {}), [p.deviceUuid]: p.status };
        renderDeviceList();
        renderWAStatus();
      } else {
        state.wa = p;
        renderWAStatus();
      }
      break;
    }
    case "ai_status_updated":
      state.ai = evt.payload;
      renderAIStatus();
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
        patchSingleStressRow(prevRow);
      }
      if (state.runner.currentRowNo) {
        patchSingleTableRow(state.runner.currentRowNo);
        patchSingleOOBRow(state.runner.currentRowNo);
        patchSingleStressRow(state.runner.currentRowNo);
      }
      break;
    }
    case "human_behavior":
      state.telemetry = evt.payload;
      renderTelemetry();
      break;
    case "channel_behavior": {
      const p = evt.payload || {};
      if (p.channels) {
        state.channelTelemetry = p.channels;
        renderTelemetry();
      }
      break;
    }
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
    case "stress_row_updated": {
      const r = evt.payload;
      state.stressMap.set(r.no, r);
      const idx = state.stressTestRows.findIndex((x) => x.no === r.no);
      if (idx >= 0) state.stressTestRows[idx] = r;
      patchSingleStressRow(r.no);
      break;
    }
    case "stress_summary_updated":
      state.stressTestSummary = evt.payload;
      renderStressSection();
      break;
    case "stress_reloaded":
      state.stressTestRows = evt.payload.rows || [];
      state.stressTestSummary = evt.payload.summary;
      state.stressMap.clear();
      for (const r of state.stressTestRows) state.stressMap.set(r.no, r);
      renderStressSection();
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
      if (evt.payload.stressTestRows) {
        state.stressTestRows = evt.payload.stressTestRows;
        state.stressTestSummary = evt.payload.stressTestSummary;
        state.stressMap.clear();
        for (const r of state.stressTestRows) state.stressMap.set(r.no, r);
      }
      populateFilterDropdowns();
      renderSummaryAndOPD();
      renderTable();
      renderOutOfBankSection();
      renderStressSection();
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
  if (el("selectAIEngine") && s.aiEngine && document.activeElement !== el("selectAIEngine")) {
    el("selectAIEngine").value = s.aiEngine;
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
  const sel = el("selectAIEngine");
  const badge = el("aiEngineBadge");
  const testBtn = el("btnTestLLM");

  const currentVal = (state.settings && state.settings.aiEngine) || (ai && ai.enabled ? "llm" : "local");

  if (sel) {
    if (ai && ai.enabled) {
      sel.disabled = false;
      sel.innerHTML = `
        <option value="llm">⚡ LLM Online (${escapeHTML(ai.model)})</option>
        <option value="local">🏠 Smart Local Engine (Offline / Heuristik)</option>
      `;
      sel.value = currentVal === "local" ? "local" : "llm";
      sel.title = `Provider: ${ai.provider} • Model: ${ai.model}`;
      if (testBtn) testBtn.disabled = false;
    } else {
      sel.disabled = true;
      sel.innerHTML = `
        <option value="local">Smart Local Engine (100% Gratis / Offline)</option>
      `;
      sel.value = "local";
      sel.title = "LLM belum diatur di .env (berjalan lokal di Go)";
      if (testBtn) testBtn.disabled = true;
    }
  }

  if (badge) {
    if (ai && ai.enabled) {
      badge.textContent = `LLM Aktif: ${ai.model}`;
      badge.title = ai.provider;
    } else {
      badge.textContent = "Smart Local Engine (100% Gratis / Offline)";
      badge.title = "Berjalan lokal di Go tanpa biaya API eksternal";
    }
  }
}

function renderWAStatus() {
  const devices = state.devices || [];
  const statuses = state.deviceStatuses || {};
  let connectedCount = 0;
  for (const d of devices) {
    const st = statuses[d.deviceUuid] || {};
    if (st.loggedIn && st.connected) connectedCount++;
  }

  const dot = el("waDot");
  const lbl = el("waLabel");
  if (devices.length === 0) {
    dot.className = "status-dot offline";
    lbl.textContent = "WA Penguji: Belum Ada Nomor";
  } else if (connectedCount > 0) {
    dot.className = "status-dot online";
    lbl.textContent = `${connectedCount} dari ${devices.length} WA Terhubung`;
  } else {
    dot.className = "status-dot connecting";
    lbl.textContent = `0 dari ${devices.length} WA Terhubung`;
  }

  const modalStatus = el("waModalStatusBox");
  if (modalStatus) {
    const active = devices.filter((d) => {
      const st = statuses[d.deviceUuid] || {};
      return st.loggedIn && st.connected;
    });
    modalStatus.innerHTML = `<strong>${active.length} perangkat aktif</strong> dari ${devices.length} terdaftar`
      + (active.length ? `<br/><span class="muted">${active.map((d) => escapeHTML(d.phoneNumber)).join(" • ")}</span>` : "");
  }

  const qrContainer = el("qrContainer");
  const qrImg = el("qrImage");
  const pairBox = el("pairingCodeBox");
  const selected = statuses[state.selectedDeviceUuid] || {};

  if (selected.qrDataUrl && !selected.connected) {
    qrImg.src = selected.qrDataUrl;
    qrContainer.classList.remove("hidden");
  } else {
    qrContainer.classList.add("hidden");
  }

  if (selected.pairingCode && !selected.connected) {
    pairBox.textContent = selected.pairingCode;
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

  renderMultiChannelGrid();
  updateLiveCountdownTick();
}

function renderMultiChannelGrid() {
  const grid = el("multiChannelGrid");
  const single = el("singleTelemetryGrid");
  if (!grid || !single) return;

  const devices = state.devices || [];
  const statuses = state.deviceStatuses || {};
  const channels = state.channelTelemetry || {};
  const activeDevs = devices.filter((d) => {
    const st = statuses[d.deviceUuid] || {};
    return st.loggedIn && st.connected;
  });

  // When 2 or more devices are connected, expand the top panel into multi-channel cards
  // and hide the global single-channel countdown to avoid duplicate/jumping timers.
  const headerCountdownBox = document.querySelector(".telemetry-countdown-box");
  const headerPhaseBarTrack = document.querySelector(".phase-progress-track");
  const humanPhaseLabel = el("humanPhaseLabel");
  const humanPhaseBadge = el("humanPhaseBadge");
  const humanPresenceBadge = el("humanPresenceBadge");

  if (activeDevs.length >= 2) {
    grid.classList.remove("hidden");
    single.classList.add("hidden");
    if (headerCountdownBox) headerCountdownBox.classList.add("hidden");
    if (headerPhaseBarTrack) headerPhaseBarTrack.classList.add("hidden");

    // Summarize aggregate multi-WA status cleanly in the header
    const busyCount = activeDevs.filter((d) => {
      const t = channels[d.deviceUuid] || {};
      return t.phase && t.phase !== "idle" && t.phase !== "paused";
    }).length;
    if (humanPhaseBadge) {
      humanPhaseBadge.textContent = "MULTI-WA AKTIF";
      humanPhaseBadge.className = "badge badge-active";
    }
    if (humanPresenceBadge) {
      humanPresenceBadge.textContent = `${activeDevs.length} Jalur Terhubung`;
    }
    if (humanPhaseLabel) {
      humanPhaseLabel.textContent = busyCount > 0
        ? `${busyCount} WA sedang memproses soal secara paralel, ${activeDevs.length - busyCount} standby`
        : `${activeDevs.length} nomor WA aktif siap menerima giliran soal paralel`;
    }

    grid.innerHTML = activeDevs
      .map((d, i) => {
        const t = channels[d.deviceUuid] || {};
        const cls = channelClass(i);
        const phase = (t.phase || "idle").toUpperCase().replaceAll("_", " ");
        const phaseLabel = t.phaseLabel || "Standby / Siap menerima giliran soal";
        const remSec = t.phaseEndsAt && t.phaseEndsAt > Date.now()
          ? ((t.phaseEndsAt - Date.now()) / 1000).toFixed(1) + " dtk"
          : "0.0 dtk";
        const activeRow = t.activeRowNo ? `• Soal #${t.activeRowNo}` : "";
        return `
          <div class="channel-card ${t.phase && t.phase !== 'idle' ? 'channel-card-active' : ''}" data-channel-uuid="${escapeHTML(d.deviceUuid)}">
            <div class="channel-card-header">
              <span class="channel-card-title ${cls}">
                <span class="dot"></span>WA-${i + 1} (${escapeHTML(d.deviceName || d.phoneNumber)})
              </span>
              <span class="channel-card-countdown" data-channel-countdown="${escapeHTML(d.deviceUuid)}">${remSec}</span>
            </div>
            <div class="channel-card-bar-track">
              <div class="channel-card-bar-fill" data-channel-bar="${escapeHTML(d.deviceUuid)}" style="width: 0%"></div>
            </div>
            <div class="channel-card-phase">
              <strong style="color:var(--text)">${escapeHTML(phase)}</strong> ${activeRow}
              <div style="font-size:11px; color:var(--text-light); margin-top:2px">${escapeHTML(phaseLabel)}</div>
            </div>
            <div class="channel-card-metrics">
              <div>Ketik: <strong>${t.lastTypingChars || 0}h</strong> (${((t.lastTypingDurationMs || 0) / 1000).toFixed(1)}s)</div>
              <div>Tunggu Bot: <strong>${((t.lastReadDurationMs || 0) / 1000).toFixed(1)}s</strong></div>
              <div>Sblm Read: <strong>${((t.lastUnreadDelayMs || 0) / 1000).toFixed(1)}s</strong></div>
              <div>Jeda: <strong>${((t.lastInterDelayMs || 0) / 1000).toFixed(1)}s</strong></div>
            </div>
          </div>
        `;
      })
      .join("");
  } else {
    grid.classList.add("hidden");
    single.classList.remove("hidden");
    if (headerCountdownBox) headerCountdownBox.classList.remove("hidden");
    if (headerPhaseBarTrack) headerPhaseBarTrack.classList.remove("hidden");
  }
}

function updateLiveCountdownTick() {
  const now = Date.now();

  // 1. Update single main banner countdown
  const t = state.telemetry;
  const cdEl = el("humanCountdown");
  const barEl = el("humanPhaseBar");
  if (cdEl && barEl) {
    if (!t || !t.phaseEndsAt || !t.phaseDurationMs || t.phase === "idle" || t.phase === "paused" || t.phase === "circuit_breaker") {
      cdEl.textContent = "0.0 dtk";
      barEl.style.width = "0%";
    } else {
      const remMs = Math.max(0, t.phaseEndsAt - now);
      const elapsed = Math.min(t.phaseDurationMs, Math.max(0, now - t.phaseStartedAt));
      const pct = Math.min(100, Math.max(0, (elapsed / t.phaseDurationMs) * 100));
      cdEl.textContent = `${(remMs / 1000).toFixed(1)} dtk`;
      barEl.style.width = `${pct.toFixed(1)}%`;
    }
  }

  // 2. Update multi-channel cards countdown for each active WA independently
  const channels = state.channelTelemetry || {};
  document.querySelectorAll("[data-channel-countdown]").forEach((cd) => {
    const uuid = cd.dataset.channelCountdown;
    const chT = channels[uuid];
    const bar = document.querySelector(`[data-channel-bar="${uuid}"]`);
    if (!chT || !chT.phaseEndsAt || !chT.phaseDurationMs || chT.phase === "idle" || chT.phase === "paused" || chT.phase === "circuit_breaker") {
      cd.textContent = "0.0 dtk";
      if (bar) bar.style.width = "0%";
    } else {
      const remMs = Math.max(0, chT.phaseEndsAt - now);
      const elapsed = Math.min(chT.phaseDurationMs, Math.max(0, now - chT.phaseStartedAt));
      const pct = Math.min(100, Math.max(0, (elapsed / chT.phaseDurationMs) * 100));
      cd.textContent = `${(remMs / 1000).toFixed(1)} dtk`;
      if (bar) bar.style.width = `${pct.toFixed(1)}%`;
    }
  });
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

  // Clean invalid selected IDs
  const validIds = new Set(state.outOfBankRows.map((r) => r.no));
  for (const id of state.oobSelectedSet) {
    if (!validIds.has(id)) state.oobSelectedSet.delete(id);
  }
  updateOOBBulkToolbar();

  const tbody = el("oobTableBody");
  if (!state.outOfBankRows || state.outOfBankRows.length === 0) {
    tbody.innerHTML = `<tr><td colspan="8" class="empty-cell">Belum ada baris uji diluar bank. Klik tombol "+ Generate Variasi Tahun 2026 dari Bank" di atas untuk membuat otomatis.</td></tr>`;
    el("checkAllOOB").checked = false;
    return;
  }

  const allSelected = state.outOfBankRows.length > 0 && state.oobSelectedSet.size === state.outOfBankRows.length;
  el("checkAllOOB").checked = allSelected;

  tbody.innerHTML = state.outOfBankRows
    .map((r) => {
      const cls = rowIsActive(r.no) ? ` class="row-running"` : "";
      return `<tr id="oob-row-${r.no}"${cls}>${buildOOBRowInnerHTML(r)}</tr>`;
    })
    .join("");
}

function updateOOBBulkToolbar() {
  const count = state.oobSelectedSet.size;
  const toolbar = el("oobBulkToolbar");
  if (!toolbar) return;
  if (count > 0) {
    toolbar.classList.remove("hidden");
    el("oobSelectedCount").textContent = String(count);
  } else {
    toolbar.classList.add("hidden");
  }
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
  const disconnectHtml = (r.notes || "").toLowerCase().includes("perangkat terputus")
    ? `<span class="badge-disconnect">⛔ Perangkat terputus 2x</span>`
    : "";

  return `
    <td class="col-no">${rowChannelBadgeHTML(r.no)}#${r.no}</td>
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
    <td class="col-time">${renderChatTimeHTML(r.metrics?.testedAt)}</td>
    <td class="col-timing">${renderTimingCardHTML(r.metrics)}</td>
    <td class="col-device">${renderDeviceDualStackHTML(r.metrics, r.no)}</td>
    <td class="col-res">
      <div class="row-actions-stack">
        <div>${resBadge} ${disconnectHtml}</div>
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

  const isChecked = state.oobSelectedSet.has(r.no) ? "checked" : "";
  const noteHtml = r.notes ? `<div class="note-preview">${escapeHTML(r.notes)}</div>` : "";
  const disconnectHtml = (r.notes || "").toLowerCase().includes("perangkat terputus")
    ? `<span class="badge-disconnect">⛔ Perangkat terputus 2x</span>`
    : "";

  return `
    <td class="col-check">
      <input type="checkbox" class="oob-row-checkbox" data-no="${r.no}" ${isChecked} />
    </td>
    <td class="col-no">${rowChannelBadgeHTML(r.no)}#${r.no}</td>
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
    <td class="col-time">${renderChatTimeHTML(r.metrics?.testedAt)}</td>
    <td class="col-timing">${renderTimingCardHTML(r.metrics)}</td>
    <td class="col-device">${renderDeviceDualStackHTML(r.metrics, r.no)}</td>
    <td class="col-res">
      <div class="row-actions-stack">
        <div>${resBadge} ${disconnectHtml}</div>
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

function buildStressRowInnerHTML(r) {
  let resBadge = `<span class="badge badge-neutral">Belum Diuji</span>`;
  if (r.resultStatus === "Cocok") {
    resBadge = `<span class="badge badge-ok">Lolos / Tahan Uji</span>`;
  } else if (r.resultStatus === "Tidak") {
    resBadge = `<span class="badge badge-bad">Gagal / Halusinasi</span>`;
  }

  const isChecked = state.stressSelectedSet.has(r.no) ? "checked" : "";
  const noteHtml = r.notes ? `<div class="note-preview">${escapeHTML(r.notes)}</div>` : "";
  const disconnectHtml = (r.notes || "").toLowerCase().includes("perangkat terputus")
    ? `<span class="badge-disconnect">⛔ Perangkat terputus 2x</span>`
    : "";

  return `
    <td class="col-check">
      <input type="checkbox" class="stress-row-checkbox" data-no="${r.no}" ${isChecked} />
    </td>
    <td class="col-no">${rowChannelBadgeHTML(r.no)}#${r.no}</td>
    <td class="col-meta">
      <div class="opd-name-cell" style="color:#5b21b6">${escapeHTML(r.stressCategory)}</div>
      <div style="font-size:11.5px; color:var(--text-light)">${escapeHTML(r.intentPurpose)}</div>
      ${r.hallucinationLvl ? `<div style="margin-top:4px"><span class="badge badge-outline">Halusinasi: ${escapeHTML(r.hallucinationLvl)}</span></div>` : ""}
    </td>
    <td class="col-q">
      <div class="q-block-label">Chat Warga (Diinput):</div>
      <div class="q-input-text" style="color:#4c1d95; background:#faf5ff; border-color:#d8b4fe"><strong>${escapeHTML(r.userPromptQ)}</strong></div>
      <div class="row-btn-group">
        <button type="button" class="btn btn-outline btn-xs" data-stress-action="edit" data-no="${r.no}">Edit</button>
        <button type="button" class="btn btn-danger-outline btn-xs" data-stress-action="del" data-no="${r.no}">Hapus</button>
      </div>
    </td>
    <td class="col-a">
      <div style="font-size:12px; color:var(--text-muted)">${escapeHTML(r.expectedBehavior)}</div>
    </td>
    <td class="col-a">${formatReplyHTML(r.actualReply, r.mediaFiles)}</td>
    <td class="col-time">${renderChatTimeHTML(r.metrics?.testedAt)}</td>
    <td class="col-timing">${renderTimingCardHTML(r.metrics)}</td>
    <td class="col-device">${renderDeviceDualStackHTML(r.metrics, r.no)}</td>
    <td class="col-res">
      <div class="row-actions-stack">
        <div>${resBadge} ${disconnectHtml}</div>
        ${noteHtml}
        <div class="row-btn-group">
          <button type="button" class="btn btn-primary btn-xs" data-stress-action="run" data-no="${r.no}" style="background:#5b21b6; border-color:#4c1d95">Uji ke WA</button>
          <button type="button" class="btn btn-outline btn-xs" data-stress-action="set-cocok" data-no="${r.no}">Lolos</button>
          <button type="button" class="btn btn-outline btn-xs" data-stress-action="set-tidak" data-no="${r.no}">Gagal</button>
        </div>
      </div>
    </td>
  `;
}

function renderStressSection() {
  const sum = state.stressTestSummary || { total: 0, cocok: 0, tidak: 0, belumDiuji: 0, progressPct: 0, resiliencePct: 0 };
  el("tabCountStress").textContent = String(sum.total || 0);
  el("kpiStressTotal").textContent = String(sum.total || 0);
  el("kpiStressTested").textContent = String(sum.tested || 0);
  el("kpiStressProgressPct").textContent = `${sum.progressPct || 0}% selesai`;
  el("kpiStressCocok").textContent = String(sum.cocok || 0);
  el("kpiStressResiliencePct").textContent = `Ketahanan AI: ${sum.resiliencePct || 0}%`;
  el("kpiStressTidak").textContent = String(sum.tidak || 0);
  el("kpiStressBelum").textContent = String(sum.belumDiuji || 0);

  const validIds = new Set(state.stressTestRows.map((r) => r.no));
  for (const id of state.stressSelectedSet) {
    if (!validIds.has(id)) state.stressSelectedSet.delete(id);
  }
  updateStressBulkToolbar();

  const tbody = el("stressTableBody");
  if (!state.stressTestRows || state.stressTestRows.length === 0) {
    tbody.innerHTML = `<tr><td colspan="8" class="empty-cell">Belum ada baris uji stress AI. Klik tombol "🎭 Generate Skenario Curhat &amp; Stress Test" di atas untuk membuat otomatis.</td></tr>`;
    el("checkAllStress").checked = false;
    return;
  }

  const allSelected = state.stressTestRows.length > 0 && state.stressSelectedSet.size === state.stressTestRows.length;
  el("checkAllStress").checked = allSelected;

  tbody.innerHTML = state.stressTestRows
    .map((r) => {
      const cls = rowIsActive(r.no) ? ` class="row-running"` : "";
      return `<tr id="stress-row-${r.no}"${cls}>${buildStressRowInnerHTML(r)}</tr>`;
    })
    .join("");
}

function updateStressBulkToolbar() {
  const count = state.stressSelectedSet.size;
  const toolbar = el("stressBulkToolbar");
  if (!toolbar) return;
  if (count > 0) {
    toolbar.classList.remove("hidden");
    el("stressSelectedCount").textContent = String(count);
  } else {
    toolbar.classList.add("hidden");
  }
}

function patchSingleStressRow(no) {
  const r = state.stressMap.get(no);
  if (!r) return;
  const existing = document.getElementById(`stress-row-${no}`);
  if (existing) {
    existing.className = rowIsActive(r.no) ? "row-running" : "";
    existing.innerHTML = buildStressRowInnerHTML(r);
  }
}


/* Channel chip helpers: map a row number to the device currently working it. */
function devicesForRow(rowNo) {
  const active = state.runner?.activeRows || {};
  const ids = Object.keys(active).filter((id) => active[id] === rowNo);
  return ids.map((id) => {
    const idx = (state.devices || []).findIndex((d) => d.deviceUuid === id);
    const dev = idx >= 0 ? state.devices[idx] : null;
    return {
      uuid: id,
      label: dev ? `WA-${idx + 1}` : `WA-${id.slice(0, 4)}`,
      phone: dev ? dev.phoneNumber : "",
      cls: channelClass(idx >= 0 ? idx : 0),
    };
  });
}

function rowChannelBadgeHTML(rowNo) {
  const list = devicesForRow(rowNo);
  if (list.length === 0) return "";
  return list
    .map((d) => `<span class="row-channel ${d.cls}" title="${escapeHTML(d.phone)}"><span class="dot"></span>${escapeHTML(d.label)}</span>`)
    .join(" ");
}

function rowIsActive(rowNo) {
  return devicesForRow(rowNo).length > 0;
}

function renderTable() {
  const tbody = el("qaTableBody");
  const filtered = state.rows.filter(rowMatchesFilter);
  el("filteredCountInfo").textContent = `Menampilkan ${filtered.length} dari ${state.rows.length} baris`;

  if (filtered.length === 0) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-cell">Tidak ada baris pertanyaan yang cocok dengan filter saat ini.</td></tr>`;
    return;
  }

  tbody.innerHTML = filtered
    .map((r) => {
      const cls = rowIsActive(r.no) ? ` class="row-running"` : "";
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
    existing.className = rowIsActive(r.no) ? "row-running" : "";
    existing.innerHTML = buildRowInnerHTML(r);
  }
}

function patchSingleOOBRow(no) {
  const r = state.oobMap.get(no);
  if (!r) return;
  const existing = document.getElementById(`oob-row-${no}`);
  if (existing) {
    existing.className = rowIsActive(r.no) ? "row-running" : "";
    existing.innerHTML = buildOOBRowInnerHTML(r);
  }
}

/* Modal Launch Batch & Real-Time Time Estimator */
let batchMode = "count"; // "count" | "range"

function setBatchModalMode(mode) {
  batchMode = mode;
  const isCount = mode === "count";
  el("radioModeCount").checked = isCount;
  el("radioModeRange").checked = !isCount;
  el("cardBatchCount").classList.toggle("active", isCount);
  el("cardBatchRange").classList.toggle("active", !isCount);
  recalculateModalEstimates();
}

function openBatchLaunchModal(targetSheet = "bank") {
  state.batchTargetSheet = targetSheet;
  let rows = state.rows;
  let sheetName = "Bank Pertanyaan";
  if (targetSheet === "out_of_bank") {
    rows = state.outOfBankRows;
    sheetName = "Uji Diluar Bank";
  } else if (targetSheet === "stress_test") {
    rows = state.stressTestRows;
    sheetName = "Uji Stress AI";
  }

  const pendingCount = rows.filter((r) => !r.resultStatus).length;
  const totalCount = rows.length;

  el("batchModalTitle").textContent = `Luncurkan Sesi Uji ${sheetName} (Aman & Anti-Spam)`;
  el("batchRecTotal").textContent = String(pendingCount > 0 ? pendingCount : totalCount);
  el("batchMaxLabel").textContent = `dari ${pendingCount > 0 ? pendingCount : totalCount} soal (${pendingCount} belum diuji)`;

  const input = el("inputBatchCount");
  input.max = String(totalCount || 1);
  const defVal = pendingCount > 0 ? Math.min(30, pendingCount) : Math.min(30, totalCount);
  input.value = String(defVal || 1);

  el("inputRangeFrom").max = String(totalCount || 1);
  el("inputRangeTo").max = String(totalCount || 1);
  el("inputRangeFrom").value = "1";
  el("inputRangeTo").value = String(Math.min(30, totalCount || 1));

  el("checkForceAllBatch").checked = pendingCount === 0;

  setBatchModalMode("count");
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

  let rows = state.rows;
  if (state.batchTargetSheet === "out_of_bank") {
    rows = state.outOfBankRows;
  } else if (state.batchTargetSheet === "stress_test") {
    rows = state.stressTestRows;
  }
  let count = 1;

  if (batchMode === "range") {
    let from = Math.max(1, Number(el("inputRangeFrom").value) || 1);
    let to = Math.max(from, Number(el("inputRangeTo").value) || from);
    if (to > rows.length && rows.length > 0) to = rows.length;
    count = Math.max(1, (to - from) + 1);
    el("rangeCountInfo").textContent = `Mencakup baris #${from} s/d #${to} (${count} soal)`;
  } else {
    count = Math.max(1, Number(el("inputBatchCount").value) || 1);
  }

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
  el("btnLaunchStressBatchModal").addEventListener("click", () => openBatchLaunchModal("stress_test"));
  el("btnCloseBatchModal").addEventListener("click", () => el("batchModal").classList.add("hidden"));
  el("btnCancelBatchModal").addEventListener("click", () => el("batchModal").classList.add("hidden"));

  // Mode Card Selection Toggle in Batch Modal
  el("cardBatchCount").addEventListener("click", () => setBatchModalMode("count"));
  el("cardBatchRange").addEventListener("click", () => setBatchModalMode("range"));
  el("radioModeCount").addEventListener("change", () => setBatchModalMode("count"));
  el("radioModeRange").addEventListener("change", () => setBatchModalMode("range"));

  el("inputBatchCount").addEventListener("input", recalculateModalEstimates);
  el("inputRangeFrom").addEventListener("input", recalculateModalEstimates);
  el("inputRangeTo").addEventListener("input", recalculateModalEstimates);

  document.querySelectorAll("[data-batch-preset]").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      setBatchModalMode("count");
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

  document.querySelectorAll("[data-range-preset]").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      setBatchModalMode("range");
      const [fromStr, toStr] = btn.dataset.rangePreset.split("-");
      el("inputRangeFrom").value = fromStr;
      el("inputRangeTo").value = toStr;
      recalculateModalEstimates();
    });
  });

  el("btnConfirmStartBatch").addEventListener("click", async () => {
    const forceAll = el("checkForceAllBatch").checked;
    const targetSheet = state.batchTargetSheet || "bank";
    let body = { targetSheet, forceAll };

    if (batchMode === "range") {
      const from = Number(el("inputRangeFrom").value) || 1;
      const to = Number(el("inputRangeTo").value) || from;
      body.rangeFrom = from;
      body.rangeTo = to;
      body.limit = 0;
    } else {
      body.limit = Number(el("inputBatchCount").value) || 30;
      body.rangeFrom = 0;
      body.rangeTo = 0;
    }

    try {
      await saveSettingsFromInputs(true);
      await api("/api/runner/start", {
        method: "POST",
        body,
      });
      el("batchModal").classList.add("hidden");
      let sheetName = "Bank Pertanyaan";
      if (targetSheet === "out_of_bank") sheetName = "Uji Diluar Bank";
      if (targetSheet === "stress_test") sheetName = "Uji Stress AI";
      const label = batchMode === "range"
        ? `rentang #${body.rangeFrom} s/d #${body.rangeTo}`
        : `${body.limit} soal`;
      toast(`Memulai pengujian batch ${sheetName} (${label})...`);
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

  // AI Diagnostics & Reload .env
  el("btnReloadEnv").addEventListener("click", async () => {
    try {
      const res = await api("/api/settings/reload-env", { method: "POST" });
      state.ai = res.ai;
      state.settings = res.settings;
      renderAIStatus();
      populateSettingsUI();
      toast(res.ai.enabled ? `File .env terbaca ulang: Aktif memakai ${res.ai.model}` : "File .env terbaca ulang (Smart Local Engine aktif).");
    } catch (err) {
      toast(`Gagal membaca ulang .env: ${err.message}`, true);
    }
  });

  el("btnTestLLM").addEventListener("click", async () => {
    const btn = el("btnTestLLM");
    btn.disabled = true;
    btn.textContent = "Menguji...";
    try {
      const res = await api("/api/llm/test", { method: "POST" });
      toast(res.message);
    } catch (err) {
      toast(`Tes LLM Gagal: ${err.message}`, true);
    } finally {
      btn.disabled = false;
      btn.textContent = "⚡ Tes Ping LLM";
    }
  });

  // Settings Save & Presets
  el("btnSaveQuickSettings").addEventListener("click", () => saveSettingsFromInputs());
  el("selectQuestionMode").addEventListener("change", () => saveSettingsFromInputs());
  if (el("selectAIEngine")) {
    el("selectAIEngine").addEventListener("change", () => saveSettingsFromInputs());
  }
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

  // Advanced Out-of-Bank Generator Modal Triggers
  el("btnOpenOOBGenModal").addEventListener("click", () => {
    const ai = state.ai;
    const badge = el("oobGenEngineBadge");
    if (ai && ai.enabled) {
      badge.textContent = `LLM Aktif: ${ai.model}`;
      badge.style.color = "#1d4ed8";
    } else {
      badge.textContent = "Smart Local Engine (Gratis / Offline)";
      badge.style.color = "#15803d";
    }
    el("oobGenModal").classList.remove("hidden");
  });

  el("btnCloseOOBGenModal").addEventListener("click", () => el("oobGenModal").classList.add("hidden"));
  el("btnCancelOOBGenModal").addEventListener("click", () => el("oobGenModal").classList.add("hidden"));

  document.querySelectorAll("[data-prompt-preset]").forEach((btn) => {
    btn.addEventListener("click", () => {
      el("oobPromptInput").value = btn.dataset.promptPreset;
    });
  });

  el("formOOBGenerate").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btnSubmit = el("btnSubmitOOBGen");
    btnSubmit.disabled = true;
    btnSubmit.textContent = "⏳ Membangkitkan Soal...";

    const promptText = el("oobPromptInput").value.trim();
    const targetYear = el("oobTargetYear").value.trim() || "2026";
    const count = Number(el("oobGenCount").value) || 20;

    const checkedScenarios = [];
    document.querySelectorAll("input[name='oobScenarioCheck']:checked").forEach((chk) => {
      checkedScenarios.push(chk.value);
    });

    try {
      const res = await api("/api/oob/generate", {
        method: "POST",
        body: {
          prompt: promptText,
          targetYear,
          count,
          scenarios: checkedScenarios,
        },
      });
      el("oobGenModal").classList.add("hidden");
      toast(`Berhasil membangkitkan ${res.rows?.length || 0} variasi soal pada sheet "Uji Diluar Bank"`);
    } catch (err) {
      toast(`Gagal membangkitkan soal: ${err.message}`, true);
    } finally {
      btnSubmit.disabled = false;
      btnSubmit.textContent = "✨ Bangkitkan Soal Sekarang";
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

  // Checkbox Selection in Out-of-Bank Sheet
  el("checkAllOOB").addEventListener("change", (e) => {
    const checked = e.target.checked;
    if (checked) {
      state.outOfBankRows.forEach((r) => state.oobSelectedSet.add(r.no));
    } else {
      state.oobSelectedSet.clear();
    }
    renderOutOfBankSection();
  });

  el("oobTableBody").addEventListener("change", (e) => {
    const chk = e.target.closest(".oob-row-checkbox");
    if (!chk) return;
    const no = Number(chk.dataset.no);
    if (chk.checked) {
      state.oobSelectedSet.add(no);
    } else {
      state.oobSelectedSet.delete(no);
    }
    updateOOBBulkToolbar();
    el("checkAllOOB").checked = state.outOfBankRows.length > 0 && state.oobSelectedSet.size === state.outOfBankRows.length;
  });

  el("btnCancelSelectOOB").addEventListener("click", () => {
    state.oobSelectedSet.clear();
    renderOutOfBankSection();
  });

  el("btnReevaluateSelectedOOB")?.addEventListener("click", async () => {
    const count = state.oobSelectedSet.size;
    if (count === 0) return;
    if (!confirm(`Evaluasi ulang ${count} soal terpilih berdasarkan balasan yang sudah ada?`)) return;
    const numbers = Array.from(state.oobSelectedSet);
    toast(`Mengevaluasi ${count} soal terpilih...`);
    try {
      const res = await api("/api/oob/reevaluate-batch", { method: "POST", body: { numbers } });
      toast(`Evaluasi selesai: ${res.total} baris diperbarui (${res.cocok} Cocok, ${res.tidak} Tidak Cocok).`);
    } catch (err) {
      toast(`Gagal evaluasi ulang: ${err.message}`, true);
    }
  });

  el("btnReevaluateAllOOB")?.addEventListener("click", async () => {
    if (!confirm("Evaluasi ulang SEMUA balasan bot yang sudah tersimpan pada sheet Uji Diluar Bank?\n\n(Proses ini berjalan di memori/database tanpa mengirim chat apa pun ke WhatsApp)")) return;
    toast("Menjalankan evaluasi ulang semua balasan...");
    try {
      const res = await api("/api/oob/reevaluate-all", { method: "POST" });
      toast(`Evaluasi selesai: ${res.total} baris diperbarui (${res.cocok} Cocok / Aman, ${res.tidak} Tidak Cocok).`);
    } catch (err) {
      toast(`Gagal evaluasi ulang: ${err.message}`, true);
    }
  });

  el("btnDeleteSelectedOOB").addEventListener("click", async () => {
    const count = state.oobSelectedSet.size;
    if (count === 0) return;
    if (!confirm(`Hapus ${count} soal terpilih dari sheet Uji Diluar Bank? (Data Bank Pertanyaan resmi tidak akan disentuh)`)) return;
    const numbers = Array.from(state.oobSelectedSet);
    try {
      await api("/api/oob/delete-batch", { method: "POST", body: { numbers } });
      state.oobSelectedSet.clear();
      toast(`Berhasil menghapus ${count} soal terpilih dari sheet Uji Diluar Bank.`);
    } catch (err) {
      toast(`Gagal menghapus: ${err.message}`, true);
    }
  });

  el("btnClearAllOOB").addEventListener("click", async () => {
    if (!confirm("⚠️ PERINGATAN: Kosongkan SELURUH soal di sheet Uji Diluar Bank? (Sheet Bank Pertanyaan resmi tetap aman)")) return;
    try {
      await api("/api/oob/clear-all", { method: "POST" });
      state.oobSelectedSet.clear();
      toast("Seluruh soal pada sheet Uji Diluar Bank berhasil dikosongkan.");
    } catch (err) {
      toast(`Gagal mengosongkan: ${err.message}`, true);
    }
  });

  // Stress Test Sheet Actions & Modals
  el("btnOpenStressGenModal").addEventListener("click", () => {
    const ai = state.ai;
    const badge = el("stressGenEngineBadge");
    if (ai && ai.enabled) {
      badge.textContent = `LLM Aktif: ${ai.model}`;
      badge.style.color = "#5b21b6";
    } else {
      badge.textContent = "Smart Local Engine (Gratis / Offline)";
      badge.style.color = "#15803d";
    }
    el("stressGenModal").classList.remove("hidden");
  });

  el("btnCloseStressGenModal").addEventListener("click", () => el("stressGenModal").classList.add("hidden"));
  el("btnCancelStressGenModal").addEventListener("click", () => el("stressGenModal").classList.add("hidden"));

  document.querySelectorAll("[data-stress-preset]").forEach((btn) => {
    btn.addEventListener("click", () => {
      el("stressPromptInput").value = btn.dataset.stressPreset;
    });
  });

  el("formStressGenerate").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btnSubmit = el("btnSubmitStressGen");
    btnSubmit.disabled = true;
    btnSubmit.textContent = "⏳ Membangkitkan Skenario Stress...";

    const promptText = el("stressPromptInput").value.trim();
    const count = Number(el("stressGenCount").value) || 15;
    const checkedCats = [];
    document.querySelectorAll("input[name='stressCatCheck']:checked").forEach((chk) => {
      checkedCats.push(chk.value);
    });

    try {
      const res = await api("/api/stress/generate", {
        method: "POST",
        body: {
          prompt: promptText,
          count,
          categories: checkedCats,
        },
      });
      el("stressGenModal").classList.add("hidden");
      toast(`Berhasil membangkitkan ${res.rows?.length || 0} skenario stress pada sheet "Uji Stress AI"`);
    } catch (err) {
      toast(`Gagal membangkitkan: ${err.message}`, true);
    } finally {
      btnSubmit.disabled = false;
      btnSubmit.textContent = "🎭 Bangkitkan Skenario Curhat & Stress Sekarang";
    }
  });

  el("btnOpenNewStressModal").addEventListener("click", () => openStressModal(0));
  el("btnCloseStressEditModal").addEventListener("click", () => el("stressEditModal").classList.add("hidden"));

  el("stressTableBody").addEventListener("click", async (e) => {
    const btn = e.target.closest("button[data-stress-action]");
    if (!btn) return;
    const no = Number(btn.dataset.no);
    const action = btn.dataset.stressAction;

    try {
      if (action === "run") {
        await saveSettingsFromInputs(true);
        await api(`/api/stress/rows/${no}/run`, { method: "POST" });
        toast(`Menguji Soal Stress AI #${no} ke WhatsApp...`);
      } else if (action === "edit") {
        openStressModal(no);
      } else if (action === "del") {
        if (!confirm(`Hapus soal Stress AI #${no}?`)) return;
        await api(`/api/stress/rows/${no}`, { method: "DELETE" });
        toast(`Soal #${no} dihapus.`);
      } else if (action === "set-cocok") {
        await api(`/api/stress/rows/${no}`, { method: "PATCH", body: { resultStatus: "Cocok" } });
        toast(`Soal Stress #${no} ditandai Lolos/Tahan Uji.`);
      } else if (action === "set-tidak") {
        await api(`/api/stress/rows/${no}`, { method: "PATCH", body: { resultStatus: "Tidak" } });
        toast(`Soal Stress #${no} ditandai Gagal/Halusinasi.`);
      }
    } catch (err) {
      toast(err.message, true);
    }
  });

  // Checkbox Selection in Stress Sheet
  el("checkAllStress").addEventListener("change", (e) => {
    const checked = e.target.checked;
    if (checked) {
      state.stressTestRows.forEach((r) => state.stressSelectedSet.add(r.no));
    } else {
      state.stressSelectedSet.clear();
    }
    renderStressSection();
  });

  el("stressTableBody").addEventListener("change", (e) => {
    const chk = e.target.closest(".stress-row-checkbox");
    if (!chk) return;
    const no = Number(chk.dataset.no);
    if (chk.checked) {
      state.stressSelectedSet.add(no);
    } else {
      state.stressSelectedSet.delete(no);
    }
    updateStressBulkToolbar();
    el("checkAllStress").checked = state.stressTestRows.length > 0 && state.stressSelectedSet.size === state.stressTestRows.length;
  });

  el("btnCancelSelectStress").addEventListener("click", () => {
    state.stressSelectedSet.clear();
    renderStressSection();
  });

  el("btnDeleteSelectedStress").addEventListener("click", async () => {
    const count = state.stressSelectedSet.size;
    if (count === 0) return;
    if (!confirm(`Hapus ${count} soal terpilih dari sheet Uji Stress AI?`)) return;
    const numbers = Array.from(state.stressSelectedSet);
    try {
      await api("/api/stress/delete-batch", { method: "POST", body: { numbers } });
      state.stressSelectedSet.clear();
      toast(`Berhasil menghapus ${count} soal terpilih dari sheet Uji Stress AI.`);
    } catch (err) {
      toast(`Gagal menghapus: ${err.message}`, true);
    }
  });

  el("btnClearAllStress").addEventListener("click", async () => {
    if (!confirm("⚠️ PERINGATAN: Kosongkan SELURUH soal di sheet Uji Stress AI?")) return;
    try {
      await api("/api/stress/clear-all", { method: "POST" });
      state.stressSelectedSet.clear();
      toast("Seluruh soal pada sheet Uji Stress AI berhasil dikosongkan.");
    } catch (err) {
      toast(`Gagal mengosongkan: ${err.message}`, true);
    }
  });

  // Stress Row Modal Form Save & Evaluate
  el("btnReevalStressModal").addEventListener("click", async () => {
    const no = Number(el("stressRowNo").value);
    if (!no) return;
    try {
      await api(`/api/stress/rows/${no}`, {
        method: "PATCH",
        body: { actualReply: el("stressActualReply").value },
      });
      const updated = await api(`/api/stress/rows/${no}/evaluate`, { method: "POST" });
      el("stressResultStatus").value = updated.resultStatus || "";
      el("stressNotes").value = updated.notes || "";
      toast(`Evaluasi ketahanan selesai -> ${updated.resultStatus}`);
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("formStressRow").addEventListener("submit", async (e) => {
    e.preventDefault();
    const no = Number(el("stressRowNo").value);
    const payload = {
      stressCategory: el("stressCategoryInput").value.trim(),
      intentPurpose: el("stressIntentInput").value.trim(),
      userPromptQ: el("stressPromptQInput").value.trim(),
      expectedBehavior: el("stressExpectedInput").value.trim(),
      actualReply: el("stressActualReply").value.trim(),
      resultStatus: el("stressResultStatus").value,
      notes: el("stressNotes").value.trim(),
    };
    try {
      if (no > 0) {
        await api(`/api/stress/rows/${no}`, { method: "PATCH", body: payload });
        toast(`Soal Stress AI #${no} diperbarui.`);
      } else {
        await api("/api/stress/rows", { method: "POST", body: payload });
        toast("Soal baru ditambahkan ke sheet Uji Stress AI.");
      }
      el("stressEditModal").classList.add("hidden");
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

  // Re-render target table immediately on tab switch to avoid blank views
  if (tabId === "tab-bank") {
    renderTable();
  } else if (tabId === "tab-oob") {
    renderOutOfBankSection();
  } else if (tabId === "tab-stress") {
    renderStressSection();
  } else if (tabId === "tab-dashboard") {
    renderSummaryAndOPD();
  }
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

function openStressModal(no) {
  el("stressRowNo").value = String(no);
  if (no > 0) {
    const r = state.stressMap.get(no);
    if (!r) return;
    el("stressEditModalTitle").textContent = `Edit Soal Uji Stress AI #${r.no}`;
    el("stressCategoryInput").value = r.stressCategory || "";
    el("stressIntentInput").value = r.intentPurpose || "";
    el("stressPromptQInput").value = r.userPromptQ || "";
    el("stressExpectedInput").value = r.expectedBehavior || "";
    el("stressActualReply").value = r.actualReply || "";
    el("stressResultStatus").value = r.resultStatus || "";
    el("stressNotes").value = r.notes || "";
  } else {
    el("stressEditModalTitle").textContent = "Tambah Soal Uji Stress AI Baru";
    el("stressCategoryInput").value = "Curhat Asmara";
    el("stressIntentInput").value = "Uji batasan empati & ruang lingkup layanan";
    el("stressPromptQInput").value = "";
    el("stressExpectedInput").value = "Bot menolak ramah & tidak larut dalam curhat pribadi, mengarahkan ke layanan resmi.";
    el("stressActualReply").value = "";
    el("stressResultStatus").value = "";
    el("stressNotes").value = "";
  }
  el("stressEditModal").classList.remove("hidden");
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
    aiEngine: el("selectAIEngine") ? el("selectAIEngine").value : "llm",
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

/* ===================== Account & Admin Management ===================== */
async function loadCurrentUser() {
  try {
    const user = await api("/api/auth/me");
    state.currentUser = user;
    el("userBadgeName").textContent = user.fullName || user.username;
    el("userBadgeRole").textContent = user.role;
    el("userModalName").textContent = user.fullName || user.username;
    el("userModalUsername").textContent = `@${user.username} • ${user.role}`;
    if (user.role === "admin") {
      el("adminUsersSection").classList.remove("hidden");
      loadAdminUsers();
    } else {
      el("adminUsersSection").classList.add("hidden");
    }
  } catch (err) {
    if (err.message.includes("401")) {
      window.location.href = BASE_PATH ? BASE_PATH + "/login.html" : "/login.html";
    }
  }
}

async function loadAdminUsers() {
  const tbody = el("adminUsersBody");
  if (!tbody) return;
  try {
    const res = await api("/api/admin/users");
    const users = res.users || [];
    tbody.innerHTML = users
      .map(
        (u) => `<tr>
          <td><strong>${escapeHTML(u.username)}</strong></td>
          <td>${escapeHTML(u.fullName || "-")}</td>
          <td class="num"><span class="badge badge-outline">${escapeHTML(u.role)}</span></td>
        </tr>`
      )
      .join("");
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="3" class="empty-cell">Gagal memuat pengguna: ${escapeHTML(err.message)}</td></tr>`;
  }
}

function bindAccountEvents() {
  el("btnUserModal").addEventListener("click", () => el("userModal").classList.remove("hidden"));
  el("btnCloseUserModal").addEventListener("click", () => el("userModal").classList.add("hidden"));

  el("btnLogout").addEventListener("click", async () => {
    try {
      await api("/api/auth/logout", { method: "POST" });
    } finally {
      window.location.href = BASE_PATH ? BASE_PATH + "/login.html" : "/login.html";
    }
  });

  const limitBtn = el("btnSetWaLimit");
  if (limitBtn) {
    limitBtn.addEventListener("click", async () => {
      const username = el("waLimitUser").value.trim();
      const limit = Number(el("waLimitValue").value) || 3;
      if (!username) {
        toast("Masukkan username terlebih dahulu.", true);
        return;
      }
      try {
        const list = await api("/api/admin/users");
        const target = (list.users || []).find((u) => u.username === username);
        if (!target) {
          toast("Username tidak ditemukan.", true);
          return;
        }
        await api("/api/admin/wa-limit", { method: "POST", body: { userId: target.id, limit } });
        toast(`Batas WA ${username} diset menjadi ${limit}.`);
      } catch (err) {
        toast(err.message, true);
      }
    });
  }

  const form = el("formCreateUser");
  if (form) {
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        username: el("newUsername").value.trim(),
        password: el("newPassword").value,
        fullName: el("newFullName").value.trim(),
        role: el("newRole").value,
      };
      try {
        await api("/api/admin/users", { method: "POST", body: payload });
        toast(`Akun ${payload.username} berhasil dibuat.`);
        form.reset();
        loadAdminUsers();
      } catch (err) {
        toast(`Gagal membuat akun: ${err.message}`, true);
      }
    });
  }
}


/* ===================== Multi-device WhatsApp manager ===================== */
function channelClass(index) {
  return `channel-${(index % 4) + 1}`;
}

function renderDeviceList() {
  const box = el("deviceList");
  if (!box) return;
  const devices = state.devices || [];
  if (devices.length === 0) {
    box.innerHTML = `<div class="empty-cell">Belum ada nomor WA. Tambahkan nomor terlebih dahulu.</div>`;
    el("btnTriggerQR").disabled = true;
    el("waLogoutControls").classList.add("hidden");
    return;
  }

  box.innerHTML = devices
    .map((d, i) => {
      const st = state.deviceStatuses[d.deviceUuid] || {};
      const online = st.loggedIn && st.connected;
      const dot = online ? "online" : st.loggedIn ? "connecting" : "offline";
      const statusText = online ? "Terhubung" : st.loggedIn ? "Sesi tersimpan" : "Belum tertaut";
      const selected = state.selectedDeviceUuid === d.deviceUuid ? " selected" : "";
      return `
        <div class="device-item${selected}" data-uuid="${escapeHTML(d.deviceUuid)}">
          <div class="device-main">
            <span class="device-name"><span class="status-dot ${dot}" style="margin-right:6px"></span>${escapeHTML(d.deviceName)}</span>
            <span class="device-phone">${escapeHTML(d.phoneNumber)} • ${escapeHTML(statusText)}</span>
          </div>
          <div class="device-actions">
            <button type="button" class="btn btn-outline btn-xs" data-device-action="select" data-uuid="${escapeHTML(d.deviceUuid)}">Pilih</button>
            <button type="button" class="btn btn-danger-outline btn-xs" data-device-action="delete" data-uuid="${escapeHTML(d.deviceUuid)}">Hapus</button>
          </div>
        </div>`;
    })
    .join("");

  const hasSelection = devices.some((d) => d.deviceUuid === state.selectedDeviceUuid);
  el("btnTriggerQR").disabled = !hasSelection;
  el("waLogoutControls").classList.toggle("hidden", !hasSelection);
}

async function loadDevices() {
  try {
    const res = await api("/api/wa/devices");
    state.devices = res.devices || [];
    state.deviceStatuses = res.statuses || {};
    if (!state.selectedDeviceUuid && state.devices.length > 0) {
      state.selectedDeviceUuid = state.devices[0].deviceUuid;
    }
    renderDeviceList();
    renderWAStatus();
  } catch (err) {
    /* keep UI quiet; the WA badge already reflects auth problems */
  }
}

function bindDeviceEvents() {
  const modal = el("waModal");
  if (!modal) return;

  el("btnWaModal").addEventListener("click", () => {
    el("waModal").classList.remove("hidden");
    loadDevices();
  });
  el("btnCloseWaModal").addEventListener("click", () => el("waModal").classList.add("hidden"));

  el("deviceList").addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-device-action]");
    const card = e.target.closest(".device-item");
    if (card) {
      state.selectedDeviceUuid = card.dataset.uuid;
      renderDeviceList();
    }
    if (!btn) return;
    const uuid = btn.dataset.uuid;
    if (btn.dataset.deviceAction === "delete") {
      if (!confirm("Hapus perangkat WA ini? Sesi login perangkat akan diputuskan.")) return;
      try {
        await api(`/api/wa/devices/${encodeURIComponent(uuid)}`, { method: "DELETE" });
        if (state.selectedDeviceUuid === uuid) state.selectedDeviceUuid = "";
        toast("Perangkat dihapus.");
        loadDevices();
      } catch (err) {
        toast(err.message, true);
      }
    }
  });

  el("formAddDevice").addEventListener("submit", async (e) => {
    e.preventDefault();
    const payload = {
      deviceName: el("newDeviceName").value.trim(),
      phone: el("newDevicePhone").value.trim(),
    };
    try {
      const res = await api("/api/wa/devices", { method: "POST", body: payload });
      state.selectedDeviceUuid = res.device?.deviceUuid || state.selectedDeviceUuid;
      el("newDeviceName").value = "";
      el("newDevicePhone").value = "";
      toast("Nomor WA ditambahkan.");
      loadDevices();
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnTriggerQR").addEventListener("click", async () => {
    if (!state.selectedDeviceUuid) {
      toast("Pilih perangkat terlebih dahulu.", true);
      return;
    }
    try {
      await api("/api/wa/connect", { method: "POST", body: { deviceUuid: state.selectedDeviceUuid } });
      toast("Menghubungkan perangkat...");
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnRequestPairing").addEventListener("click", async () => {
    const phone = el("inputPairingPhone").value.trim();
    if (!state.selectedDeviceUuid) {
      toast("Pilih perangkat terlebih dahulu.", true);
      return;
    }
    try {
      const res = await api("/api/wa/pair", {
        method: "POST",
        body: { phone, deviceUuid: state.selectedDeviceUuid },
      });
      toast(`Kode Pairing: ${res.pairingCode}`);
    } catch (err) {
      toast(err.message, true);
    }
  });

  el("btnLogoutWA").addEventListener("click", async () => {
    if (!state.selectedDeviceUuid) return;
    if (!confirm("Putuskan sesi login perangkat terpilih?")) return;
    try {
      await api("/api/wa/logout", { method: "POST", body: { deviceUuid: state.selectedDeviceUuid } });
      toast("Sesi perangkat diputuskan.");
      loadDevices();
    } catch (err) {
      toast(err.message, true);
    }
  });
}

bindDeviceEvents();
loadDevices();

bindAccountEvents();
loadCurrentUser();

function setupExportModal() {
  const btnOpen = el("btnDownloadExcel");
  const modal = el("modalExportExcel");
  const btnClose = el("btnCloseExportModal");
  const btnCancel = el("btnCancelExport");
  const btnExecute = el("btnExecuteExport");

  if (!btnOpen || !modal) return;

  btnOpen.addEventListener("click", (e) => {
    e.preventDefault();
    modal.classList.remove("hidden");
  });

  const closeModal = () => modal.classList.add("hidden");
  if (btnClose) btnClose.addEventListener("click", closeModal);
  if (btnCancel) btnCancel.addEventListener("click", closeModal);
  modal.addEventListener("click", (e) => {
    if (e.target === modal) closeModal();
  });

  const dateFrom = el("expDateFrom");
  const dateTo = el("expDateTo");
  const fmtDateTime = (d, timeStr = "00:00") => {
    const pad = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${timeStr}`;
  };

  el("expQuickAll")?.addEventListener("click", () => {
    if (dateFrom) dateFrom.value = "";
    if (dateTo) dateTo.value = "";
  });
  el("expQuickToday")?.addEventListener("click", () => {
    const now = new Date();
    if (dateFrom) dateFrom.value = fmtDateTime(now, "00:00");
    if (dateTo) dateTo.value = fmtDateTime(now, "23:59");
  });
  el("expQuick7d")?.addEventListener("click", () => {
    const now = new Date();
    const past = new Date(now.getTime() - 7 * 86400000);
    if (dateFrom) dateFrom.value = fmtDateTime(past, "00:00");
    if (dateTo) dateTo.value = fmtDateTime(now, "23:59");
  });
  el("expQuick30d")?.addEventListener("click", () => {
    const now = new Date();
    const past = new Date(now.getTime() - 30 * 86400000);
    if (dateFrom) dateFrom.value = fmtDateTime(past, "00:00");
    if (dateTo) dateTo.value = fmtDateTime(now, "23:59");
  });

  if (btnExecute) {
    btnExecute.addEventListener("click", () => {
      const sheets = [];
      if (el("expSheetBank")?.checked) sheets.push("bank");
      if (el("expSheetLiveChat")?.checked) sheets.push("livechat");
      if (el("expSheetOOB")?.checked) sheets.push("oob");
      if (el("expSheetStress")?.checked) sheets.push("stress");
      if (el("expSheetSummary")?.checked) sheets.push("summary");

      if (sheets.length === 0) {
        toast("Pilih minimal satu lembar kerja (sheet) untuk diunduh.", true);
        return;
      }

      const category = el("expCategoryMode")?.value || "all";
      const status = el("expStatus")?.value || "all";
      const df = dateFrom?.value || "";
      const dt = dateTo?.value || "";

      const params = new URLSearchParams();
      params.set("sheets", sheets.join(","));
      if (category !== "all") params.set("category", category);
      if (status !== "all") params.set("status", status);
      if (df) params.set("date_from", df);
      if (dt) params.set("date_to", dt);

      closeModal();
      toast("Menyiapkan unduhan Excel sesuai filter...");
      const dlUrl = (BASE_PATH ? BASE_PATH : "") + `/api/excel/download?${params.toString()}`;
      window.location.href = dlUrl;
    });
  }
}

setupExportModal();
