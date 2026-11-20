// Live Chat Emulator — frontend module. Independent from /app.js: it hydrates
// #livechatRoot only, listens to /api/livechat/events (a broker-backed SSE
// stream), and never mutates state used by the other tabs.

const LC = {
  root: null,
  userId: 0,
  preview: null,
  devices: [],
  assets: [],
  selectedAssetIds: new Set(),
  sessions: [],
  liveSessions: new Map(), // sessionId -> {session, messages, turns, typing}
  activeDetailId: 0,
  eventsAbort: null,
};

// --- tiny DOM helpers ---
function h(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
}
function escapeText(v) {
  return String(v == null ? "" : v)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
function toastLC(msg, isError) {
  const wrap = document.getElementById("toastContainer");
  if (!wrap) { console.warn("[livechat]", msg); return; }
  const item = document.createElement("div");
  item.className = "toast" + (isError ? " error" : "");
  item.textContent = msg;
  wrap.appendChild(item);
  setTimeout(() => item.remove(), 5000);
}
async function apiLC(path, options = {}) {
  const opts = { ...options, headers: { ...(options.headers || {}) } };
  if (opts.body && !(opts.body instanceof FormData) && typeof opts.body === "object") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, opts);
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = { raw: text }; }
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`);
  }
  return data;
}

// --- boot ---
async function initLiveChat() {
  LC.root = document.getElementById("livechatRoot");
  if (!LC.root) return;
  try {
    const me = await apiLC("/api/auth/me");
    LC.userId = me && me.id ? me.id : 0;
  } catch {
    // Not fatal: the emulator still works without media previews, and the
    // server itself will reject any unauthorized call anyway.
  }
  renderShell();
  bindEvents();
  refreshPreview();
  loadDevices();
  loadAssets();
  loadSessions();
  connectEvents();
  // Lazy restart: the tab may be shown long after the first request (server
  // restart, network hiccup). We retry every 15s when the stream drops.
  setInterval(() => {
    if (!LC.eventsAbort) connectEvents();
  }, 15000);
}

// --- shell (control panel + emulator grid + history) ---
function renderShell() {
  LC.root.innerHTML = `
    <div class="lc-card lc-header">
      <div>
        <h2>Live Chat Emulator</h2>
        <p>Menguji alur percakapan pengaduan Bot WhatsApp Pemko Medan secara otomatis. Satu kartu per nomor WA penguji (maks 3).</p>
      </div>
      <div class="lc-header-actions">
        <button id="lcBtnReload" type="button" class="btn btn-outline btn-sm">Muat Ulang Riwayat</button>
      </div>
    </div>

    <div class="lc-card" id="lcControl">
      <h3 class="lc-section-title">Panel Kontrol Sesi Baru</h3>
      <form id="lcForm" class="lc-form-grid">
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcDevice">Nomor WA Penguji (connected)</label></div>
          <select id="lcDevice" required></select>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcBotPhone">Nomor Bot Target</label></div>
          <input id="lcBotPhone" type="text" placeholder="Kosongkan untuk pakai pengaturan akun" />
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcMode">Kategori Mode</label></div>
          <select id="lcMode">
            <option value="benar">Benar (jawaban sesuai data)</option>
            <option value="typo">Typo (typo jari wajar)</option>
            <option value="ngasal">Ngasal (jawaban di luar konteks)</option>
            <option value="celah">Cari Celah (uji batas logika)</option>
          </select>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcMaxTurns">Batas Giliran</label></div>
          <input id="lcMaxTurns" type="number" min="1" max="200" value="40" />
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcTitle">Judul Pengaduan</label></div>
          <input id="lcTitle" type="text" required maxlength="200" />
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcReport">Isi Laporan / Pengaduan</label></div>
          <textarea id="lcReport" rows="3" required></textarea>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcNama">Nama Pengadu</label><span id="lcTagNama" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcNama" type="text" required /><button type="button" class="lc-mini-btn" data-reroll="nama">Acak ulang</button></div>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcTanggal">Tanggal Kejadian</label><span id="lcTagTanggal" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcTanggal" type="date" required /><button type="button" class="lc-mini-btn" data-reroll="tanggal">Acak ulang</button></div>
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcAlamat">Alamat Kejadian</label><span id="lcTagAlamat" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcAlamat" type="text" required /><button type="button" class="lc-mini-btn" data-reroll="alamat">Acak ulang</button></div>
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcTitik">Titik Lokasi (link maps atau lat,lng)</label><span id="lcTagTitik" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcTitik" type="text" placeholder="Kosongkan bila penguji tidak ingin share lokasi" /><button type="button" class="lc-mini-btn" data-reroll="titik">Acak ulang</button></div>
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label>Aset Berkas Penguji</label></div>
          <div id="lcAssetsList"><span class="muted">Belum ada aset. Unggah foto/dokumen di bawah.</span></div>
          <div class="lc-input-actions" style="margin-top:6px">
            <input id="lcAssetFile" type="file" accept="image/png,image/jpeg,application/pdf,.docx,.xlsx,.pptx" />
            <input id="lcAssetLabel" type="text" placeholder="Label (mis. Foto Jalan Rusak)" style="min-width:180px" />
            <button id="lcBtnUploadAsset" type="button" class="btn btn-outline btn-sm">Unggah Berkas Baru</button>
          </div>
        </div>
      </form>
      <div class="lc-actions-row">
        <button id="lcBtnStart" type="button" class="btn btn-primary">Mulai Sesi</button>
        <span id="lcHelp" class="muted" style="font-size:12px;"></span>
      </div>
    </div>

    <div class="lc-card">
      <h3 class="lc-section-title">Emulator Chat</h3>
      <div id="lcEmulatorGrid" class="lc-emulator-grid"></div>
    </div>

    <div class="lc-card">
      <div class="lc-label-row" style="margin-bottom:6px">
        <h3 class="lc-section-title" style="margin:0">Riwayat Sesi</h3>
        <button id="lcBtnRefreshHistory" type="button" class="lc-mini-btn">Muat ulang</button>
      </div>
      <div class="table-wrapper">
        <table class="lc-history-table">
          <thead>
            <tr>
              <th>No</th><th>Tanggal</th><th>Judul</th><th>Mode</th>
              <th>Giliran</th><th>Status</th><th>Tiket</th>
            </tr>
          </thead>
          <tbody id="lcHistoryBody"><tr><td colspan="7" class="muted">Memuat...</td></tr></tbody>
        </table>
      </div>
      <div id="lcDetailPanel" class="lc-detail-panel" hidden></div>
    </div>
  `;
}

// --- api calls ---
async function refreshPreview() {
  try {
    LC.preview = await apiLC("/api/livechat/preview");
    fillPreviewInputs(LC.preview);
  } catch (err) {
    toastLC("Gagal memuat isian acak: " + err.message, true);
  }
}

async function loadDevices() {
  try {
    const data = await apiLC("/api/livechat/devices");
    LC.devices = (data.devices || []).filter((d) => d.connected);
    renderDeviceOptions();
  } catch (err) {
    toastLC("Gagal memuat perangkat WA: " + err.message, true);
  }
}

async function loadAssets() {
  try {
    const data = await apiLC("/api/livechat/assets");
    LC.assets = data.assets || [];
    renderAssetList();
  } catch (err) {
    toastLC("Gagal memuat aset: " + err.message, true);
  }
}

async function loadSessions() {
  try {
    const data = await apiLC("/api/livechat/sessions");
    LC.sessions = data.sessions || [];
    renderHistory();
    // Preload active ones into memory (for the grid). Only sessions still
    // running, plus the last 3 finished, to keep initial load light.
    const candidates = LC.sessions
      .filter((s) => s.status === "running")
      .slice(0, 3);
    for (const s of candidates) {
      if (!LC.liveSessions.has(s.id)) await loadSessionDetail(s.id);
    }
    renderEmulatorGrid();
  } catch (err) {
    toastLC("Gagal memuat riwayat sesi: " + err.message, true);
  }
}

async function loadSessionDetail(id) {
  const detail = await apiLC(`/api/livechat/sessions/${id}`);
  LC.liveSessions.set(id, {
    session: detail.session,
    messages: detail.messages || [],
    turns: detail.turns || [],
    typing: false,
  });
  return LC.liveSessions.get(id);
}

// --- renderers ---
function renderDeviceOptions() {
  const sel = document.getElementById("lcDevice");
  if (!sel) return;
  if (LC.devices.length === 0) {
    sel.innerHTML = `<option value="">Tidak ada perangkat WA terhubung</option>`;
    return;
  }
  sel.innerHTML = LC.devices
    .map((d) => `<option value="${escapeText(d.deviceUuid)}">${escapeText(d.phoneNumber || d.deviceUuid)}</option>`)
    .join("");
}

function fillPreviewInputs(p) {
  if (!p) return;
  setInput("lcNama", p.namaPengadu, "nama", p.titikLokasiSource ? "random" : "random");
  setInput("lcTanggal", p.tanggalKejadian, "tanggal", "random");
  setInput("lcAlamat", p.alamatKejadian, "alamat", "random");
  setInput("lcTitik", p.titikLokasi, "titik", "random");
}
function setInput(id, value, tagKey, tagKind) {
  const inp = document.getElementById(id);
  if (!inp) return;
  inp.value = value || "";
  setTag(tagKey, tagKind);
}
function setTag(key, kind) {
  const el = document.getElementById("lcTag" + key.charAt(0).toUpperCase() + key.slice(1));
  if (!el) return;
  el.classList.remove("random", "manual", "empty");
  el.classList.add(kind);
  el.textContent = kind === "random" ? "acak" : kind === "empty" ? "kosong" : "diubah";
}

function renderAssetList() {
  const wrap = document.getElementById("lcAssetsList");
  if (!wrap) return;
  if (LC.assets.length === 0) {
    wrap.innerHTML = `<span class="muted">Belum ada aset. Unggah foto/dokumen di bawah.</span>`;
    return;
  }
  wrap.innerHTML = LC.assets
    .map((a) => {
      const sel = LC.selectedAssetIds.has(a.id) ? " selected" : "";
      const preview = a.kind === "image"
        ? `<img class="lc-asset-preview" src="/api/livechat/assets/${a.id}/preview" alt="" loading="lazy" />`
        : `<span class="lc-asset-preview" style="display:flex;align-items:center;justify-content:center;font-size:9px;background:#fee2e2;color:#b91c1c;font-weight:700">DOC</span>`;
      return `<span class="lc-asset-chip${sel}" data-asset-id="${a.id}">
        ${preview}
        <strong>${escapeText(a.label)}</strong>
        <span class="muted">${escapeText((a.mimetype || "").split("/").pop() || "")}</span>
        <button type="button" class="lc-x" data-del-asset="${a.id}" title="Hapus">&times;</button>
      </span>`;
    })
    .join("");
}

function renderEmulatorGrid() {
  const grid = document.getElementById("lcEmulatorGrid");
  if (!grid) return;
  const sessions = [...LC.liveSessions.values()];
  if (sessions.length === 0) {
    grid.innerHTML = `<div class="lc-empty lc-span-2">Belum ada sesi aktif. Isi panel kontrol di atas lalu tekan "Mulai Sesi".</div>`;
    return;
  }
  // Newest first, max 3 (plan §11.2).
  sessions.sort((a, b) => (b.session.id || 0) - (a.session.id || 0));
  grid.innerHTML = "";
  sessions.slice(0, 3).forEach((s) => grid.appendChild(buildChatCard(s)));
}

function buildChatCard(state) {
  const s = state.session;
  const card = h(`
    <div class="lc-chat-card" data-session-id="${s.id}">
      <div class="lc-chat-header">
        <div class="lc-avatar">${escapeText(initials(s.waPhone))}</div>
        <div class="lc-meta">
          <span class="lc-phone">${escapeText(s.waPhone || s.deviceUuid)}</span>
          <span class="lc-sub">${escapeText(s.title || "Pengaduan")} &bull; bot ${escapeText(s.botPhone || "-")}</span>
        </div>
        <span class="lc-chat-badge ${escapeText(s.status)}">${escapeText(s.status)}</span>
      </div>
      <div class="lc-chat-body" data-body-id="${s.id}"></div>
      <div class="lc-chat-footer">
        <button type="button" class="btn btn-outline btn-sm" data-stop-id="${s.id}" ${s.status !== "running" ? "disabled" : ""}>Stop</button>
        <button type="button" class="btn btn-outline btn-sm" data-log-id="${s.id}">Lihat Log</button>
      </div>
    </div>
  `);
  const body = card.querySelector(".lc-chat-body");
  state.messages.forEach((m) => body.appendChild(buildBubble(m)));
  if (state.typing) body.appendChild(buildTypingIndicator());
  body.scrollTop = body.scrollHeight;
  return card;
}

function buildBubble(m) {
  const kind = m.msgType || m.msg_type || "text";
  const dir = m.direction || "sent";
  const time = formatClock(m.sentAt || m.sent_at);
  const inner = renderBubbleContent(m, kind);
  return h(`<div class="lc-bubble ${dir}">${inner}<span class="lc-time">${time}</span></div>`);
}
function renderBubbleContent(m, kind) {
  const text = escapeText(m.body || m.bodyText || "");
  const media = parseMedia(m.mediaJson || m.media_json);
  if (kind === "image") {
    const file = media[0] && media[0].file;
    const src = file ? assetPreviewURL(file) : "";
    const img = src ? `<img class="lc-media-thumb" src="${src}" alt="lampiran" />` : "";
    return (text ? `<span>${text}</span>` : "") + img;
  }
  if (kind === "document") {
    const name = media[0] && (media[0].file || "dokumen");
    return `<span class="lc-bubble-title">Berkas</span>${text}
      <div class="lc-media-card"><span class="lc-doc-icon">DOC</span><span>${escapeText(name)}</span></div>`;
  }
  if (kind === "location") {
    return `<span class="lc-bubble-title">Lokasi</span>${text}
      <div class="lc-loc-card">📍 Lokasi dikirimkan</div>`;
  }
  return text;
}

function buildTypingIndicator() {
  return h(`<div class="lc-typing" aria-label="Bot sedang mengetik"><span></span><span></span><span></span></div>`);
}

function renderHistory() {
  const body = document.getElementById("lcHistoryBody");
  if (!body) return;
  if (LC.sessions.length === 0) {
    body.innerHTML = `<tr><td colspan="7" class="muted">Belum ada sesi.</td></tr>`;
    return;
  }
  body.innerHTML = LC.sessions
    .map((s, idx) => `
      <tr data-detail-id="${s.id}">
        <td>${idx + 1}</td>
        <td>${formatDate(s.createdAt)}</td>
        <td>${escapeText(s.title || "")}</td>
        <td>${escapeText(s.categoryMode || "")}</td>
        <td>${s.totalTurns || 0}</td>
        <td><span class="lc-chat-badge ${escapeText(s.status || "")}">${escapeText(s.status || "")}</span></td>
        <td>${escapeText(s.ticketNo || "")}</td>
      </tr>
    `)
    .join("");
}

function renderDetailPanel(detail) {
  const panel = document.getElementById("lcDetailPanel");
  if (!panel) return;
  if (!detail) { panel.hidden = true; panel.innerHTML = ""; return; }
  const s = detail.session;
  const rowsHtml = (detail.turns || []).map((t) => `
    <tr>
      <td>${t.turnNo}</td>
      <td>${escapeText(t.questionKind || "")}</td>
      <td>${escapeText(t.botQuestion || "")}</td>
      <td>${escapeText(t.ourAnswer || "")}</td>
      <td>${escapeText(t.matchStatus || "")}</td>
    </tr>
  `).join("");
  panel.innerHTML = `
    <h3>Detail Sesi #${s.id} &mdash; ${escapeText(s.title || "")}</h3>
    <div class="lc-detail-meta">
      <div>Status: <strong>${escapeText(s.status || "")}</strong></div>
      <div>Mode: <strong>${escapeText(s.categoryMode || "")}</strong></div>
      <div>Giliran: <strong>${s.totalTurns || 0}</strong></div>
      <div>Nomor WA: <strong>${escapeText(s.waPhone || "")}</strong></div>
      <div>Bot: <strong>${escapeText(s.botPhone || "")}</strong></div>
      <div>Tiket: <strong>${escapeText(s.ticketNo || "-")}</strong></div>
      <div style="grid-column: span 3">Alasan Selesai: <strong>${escapeText(s.endReason || "-")}</strong></div>
      <div style="grid-column: span 3">Chat Terakhir: <em>${escapeText(s.lastChatExcerpt || "-")}</em></div>
    </div>
    <div class="table-wrapper">
      <table class="lc-history-table">
        <thead><tr><th>#</th><th>Jenis</th><th>Pertanyaan Bot</th><th>Jawaban Kita</th><th>Status</th></tr></thead>
        <tbody>${rowsHtml || '<tr><td colspan="5" class="muted">Belum ada log per giliran.</td></tr>'}</tbody>
      </table>
    </div>
  `;
  panel.hidden = false;
}

// --- actions ---
async function startSession() {
  const deviceUuid = document.getElementById("lcDevice").value;
  const mode = document.getElementById("lcMode").value;
  const maxTurns = parseInt(document.getElementById("lcMaxTurns").value, 10) || 40;
  const body = {
    deviceUuid,
    botPhone: document.getElementById("lcBotPhone").value.trim(),
    title: document.getElementById("lcTitle").value.trim(),
    reportText: document.getElementById("lcReport").value.trim(),
    namaPengadu: document.getElementById("lcNama").value.trim(),
    tanggalKejadian: document.getElementById("lcTanggal").value,
    alamatKejadian: document.getElementById("lcAlamat").value.trim(),
    titikLokasi: document.getElementById("lcTitik").value.trim(),
    titikLokasiSource: document.getElementById("lcTitik").value.trim() === "" ? "empty" : "manual",
    assetIds: [...LC.selectedAssetIds],
    maxTurns,
    categoryMode: mode,
  };
  if (!body.deviceUuid) { toastLC("Pilih nomor WA penguji terlebih dahulu.", true); return; }
  if (!body.title) { toastLC("Judul pengaduan wajib diisi.", true); return; }
  if (!body.reportText) { toastLC("Isi laporan wajib diisi.", true); return; }
  if (!body.namaPengadu) { toastLC("Nama pengadu wajib diisi.", true); return; }
  if (!body.tanggalKejadian) { toastLC("Tanggal kejadian wajib diisi.", true); return; }
  if (!body.alamatKejadian) { toastLC("Alamat kejadian wajib diisi.", true); return; }

  try {
    const data = await apiLC("/api/livechat/sessions", { method: "POST", body });
    toastLC("Sesi dimulai. Pantau emulator di bawah.");
    LC.liveSessions.set(data.session.id, {
      session: data.session, messages: [], turns: [], typing: false,
    });
    renderEmulatorGrid();
    await loadSessions();
  } catch (err) {
    toastLC("Gagal memulai sesi: " + err.message, true);
  }
}

async function stopSession(id) {
  if (!confirm("Hentikan sesi ini?")) return;
  try {
    await apiLC(`/api/livechat/sessions/${id}/stop`, { method: "POST" });
    toastLC("Perintah stop dikirim.");
  } catch (err) {
    toastLC("Gagal stop: " + err.message, true);
  }
}

async function uploadAsset() {
  const input = document.getElementById("lcAssetFile");
  const label = document.getElementById("lcAssetLabel").value.trim();
  if (!input.files || input.files.length === 0) { toastLC("Pilih berkas terlebih dahulu.", true); return; }
  const form = new FormData();
  form.append("file", input.files[0]);
  if (label) form.append("label", label);
  try {
    const data = await apiLC("/api/livechat/assets", { method: "POST", body: form });
    toastLC(data.created ? "Berkas diunggah." : "Berkas sudah pernah diunggah sebelumnya.");
    input.value = "";
    document.getElementById("lcAssetLabel").value = "";
    await loadAssets();
  } catch (err) {
    toastLC("Gagal mengunggah: " + err.message, true);
  }
}

async function deleteAsset(id) {
  if (!confirm("Hapus aset ini? Ditolak bila masih dipakai sesi.")) return;
  try {
    await apiLC(`/api/livechat/assets/${id}`, { method: "DELETE" });
    LC.selectedAssetIds.delete(id);
    await loadAssets();
  } catch (err) {
    toastLC("Gagal menghapus: " + err.message, true);
  }
}

async function openDetail(id) {
  try {
    const detail = await apiLC(`/api/livechat/sessions/${id}`);
    LC.activeDetailId = id;
    renderDetailPanel(detail);
  } catch (err) {
    toastLC("Gagal memuat detail: " + err.message, true);
  }
}

// --- SSE ---
function connectEvents() {
  if (LC.eventsAbort) return;
  const ctrl = new AbortController();
  LC.eventsAbort = ctrl;
  (async () => {
    try {
      const res = await fetch("/api/livechat/events", { credentials: "same-origin", signal: ctrl.signal });
      if (!res.ok || !res.body) throw new Error("stream " + res.status);
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) throw new Error("end");
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
          const chunk = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          const line = chunk.split("\n").find((l) => l.startsWith("data:"));
          if (!line) continue;
          try { handleSSE(JSON.parse(line.slice(5).trim())); } catch { /* ignore malformed */ }
        }
      }
    } catch {
      /* fallthrough: allow reconnect */
    } finally {
      LC.eventsAbort = null;
      setTimeout(() => { if (!LC.eventsAbort) connectEvents(); }, 5000);
    }
  })();
}

function handleSSE(evt) {
  if (!evt || !evt.type) return;
  if (!evt.type.startsWith("livechat_")) return;
  const p = evt.payload || {};
  const sid = p.sessionId;
  if (!sid) return;
  const state = LC.liveSessions.get(sid);
  if (evt.type === "livechat_message" && state) {
    state.messages.push({
      direction: p.direction,
      msgType: p.msgType,
      body: p.body,
      mediaJson: p.mediaJson,
      sentAt: new Date().toISOString(),
    });
    if (p.direction === "sent") state.typing = false;
    if (p.direction === "received") state.typing = false;
    refreshCardAndScroll(sid);
    return;
  }
  if (evt.type === "livechat_turn" && state) {
    state.turns.push(p);
    state.typing = true; // anticipating the next bot reply
    refreshCardAndScroll(sid);
    return;
  }
  if (evt.type === "livechat_status") {
    loadSessions();
    if (state) {
      state.session.status = p.status;
      state.typing = false;
      renderEmulatorGrid();
      if (LC.activeDetailId === sid) openDetail(sid);
    }
    return;
  }
}

function refreshCardAndScroll(sid) {
  const card = document.querySelector(`.lc-chat-card[data-session-id="${sid}"]`);
  const state = LC.liveSessions.get(sid);
  if (!card || !state) return;
  const body = card.querySelector(`.lc-chat-body[data-body-id="${sid}"]`);
  const typing = body.querySelector(".lc-typing");
  if (typing) typing.remove();
  const last = state.messages[state.messages.length - 1];
  if (last) body.appendChild(buildBubble(last));
  if (state.typing) body.appendChild(buildTypingIndicator());
  body.scrollTop = body.scrollHeight;
}

// --- events binding ---
function bindEvents() {
  document.getElementById("lcBtnStart").addEventListener("click", startSession);
  document.getElementById("lcBtnReload").addEventListener("click", () => { loadDevices(); loadAssets(); loadSessions(); });
  document.getElementById("lcBtnRefreshHistory").addEventListener("click", loadSessions);
  document.getElementById("lcBtnUploadAsset").addEventListener("click", uploadAsset);

  document.querySelectorAll("[data-reroll]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const key = btn.dataset.reroll;
      const map = {
        nama: ["lcNama", "nama", "namaPengadu"],
        tanggal: ["lcTanggal", "tanggal", "tanggalKejadian"],
        alamat: ["lcAlamat", "alamat", "alamatKejadian"],
        titik: ["lcTitik", "titik", "titikLokasi"],
      };
      const [id, tag, field] = map[key] || [];
      if (!id || !LC.preview) return;
      document.getElementById(id).value = LC.preview[field] || "";
      setTag(tag, "random");
    });
  });

  ["lcNama", "lcTanggal", "lcAlamat", "lcTitik"].forEach((id) => {
    const input = document.getElementById(id);
    input.addEventListener("input", () => {
      const tagKey = id === "lcNama" ? "nama" : id === "lcTanggal" ? "tanggal" : id === "lcAlamat" ? "alamat" : "titik";
      if (id === "lcTitik" && input.value.trim() === "") {
        setTag(tagKey, "empty");
      } else {
        setTag(tagKey, "manual");
      }
    });
  });

  // Asset selection click + delete (delegated to survive re-renders).
  document.getElementById("lcAssetsList").addEventListener("click", (e) => {
    const del = e.target.closest("[data-del-asset]");
    if (del) {
      e.stopPropagation();
      deleteAsset(parseInt(del.dataset.delAsset, 10));
      return;
    }
    const chip = e.target.closest("[data-asset-id]");
    if (!chip) return;
    const id = parseInt(chip.dataset.assetId, 10);
    if (LC.selectedAssetIds.has(id)) LC.selectedAssetIds.delete(id);
    else LC.selectedAssetIds.add(id);
    renderAssetList();
  });

  // Emulator card actions (delegated).
  document.getElementById("lcEmulatorGrid").addEventListener("click", (e) => {
    const stop = e.target.closest("[data-stop-id]");
    if (stop) { stopSession(parseInt(stop.dataset.stopId, 10)); return; }
    const log = e.target.closest("[data-log-id]");
    if (log) openDetail(parseInt(log.dataset.logId, 10));
  });

  // History table row click.
  document.getElementById("lcHistoryBody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-detail-id]");
    if (!tr) return;
    openDetail(parseInt(tr.dataset.detailId, 10));
  });
}

// --- formatting helpers ---
function initials(phone) {
  const p = String(phone || "?").replace(/\D/g, "");
  return (p.slice(-2) || "WA").toUpperCase();
}
function formatClock(ts) {
  if (!ts) return "";
  const d = typeof ts === "string" ? new Date(ts) : new Date(ts * (ts < 1e12 ? 1000 : 1));
  if (isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("id-ID", { hour: "2-digit", minute: "2-digit" });
}
function formatDate(ts) {
  if (!ts) return "-";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("id-ID", { day: "2-digit", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
}
function parseMedia(raw) {
  if (!raw) return [];
  try { const v = JSON.parse(raw); return Array.isArray(v) ? v : []; } catch { return []; }
}
function assetPreviewURL(file) {
  // Received bot media lives under data/users/{id}/livechat_media and is
  // served by the existing /media/{user}/{file} route. LC.userId is loaded
  // from /api/auth/me on boot so the path matches the current tenant.
  if (!LC.userId) return "";
  const parts = String(file || "").split("/");
  const base = parts[parts.length - 1];
  if (!base) return "";
  return `/media/${LC.userId}/${encodeURIComponent(base)}`;
}

// The parent app may fire its tab switch through the same .tab-btn listener.
// Our emulator only cares about being visible so it can refresh state lazily.
document.addEventListener("click", (e) => {
  const btn = e.target.closest('.tab-btn[data-tab="tab-livechat"]');
  if (!btn) return;
  // Defer a tick so the section is visible before we paint.
  setTimeout(() => { loadDevices(); loadAssets(); loadSessions(); }, 0);
});

window.addEventListener("beforeunload", () => {
  if (LC.eventsAbort) LC.eventsAbort.abort();
});

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", initLiveChat);
} else {
  initLiveChat();
}
