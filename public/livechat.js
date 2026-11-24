// Live Chat Emulator — frontend module. Independent from /app.js: it hydrates
// #livechatRoot only, listens to /api/livechat/events (a broker-backed SSE
// stream), and never mutates state used by the other tabs.

const LC = {
  root: null,
  userId: 0,
  defaultBotPhone: "",
  preview: null,
  devices: [],
  assets: [],
  selectedAssetIds: new Set(),
  sessions: [],
  liveSessions: new Map(), // sessionId -> {session, messages, turns, typing}
  activeDetailId: 0,
  activeDetailData: null,
  detailViewTab: "chat", // "chat" or "audit"
  pagination: {
    page: 1,
    pageSize: 10,
    total: 0,
    totalPages: 1,
    status: "all",
    mode: "all",
    query: "",
    sort: "date_desc",
  },
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
const BASE_PATH_LC = window.location.pathname.replace(/\/(?:index\.html)?$/, "").replace(/\/$/, "");

async function apiLC(path, options = {}) {
  const opts = { ...options, headers: { ...(options.headers || {}) } };
  if (opts.body && !(opts.body instanceof FormData) && typeof opts.body === "object") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const fullPath = (BASE_PATH_LC && path.startsWith("/") && !path.startsWith(BASE_PATH_LC + "/"))
    ? (BASE_PATH_LC + path)
    : path;
  const res = await fetch(fullPath, opts);
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
    // Not fatal
  }
  try {
    const snap = await apiLC("/api/state");
    if (snap && snap.settings && snap.settings.botPhone) {
      LC.defaultBotPhone = snap.settings.botPhone;
    }
  } catch {
    // Not fatal
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
        <button id="lcBtnDownloadExcel" type="button" class="btn btn-outline btn-sm">Download Excel</button>
        <button id="lcBtnReload" type="button" class="btn btn-outline btn-sm">Muat Ulang Riwayat</button>
      </div>
    </div>

    <div class="lc-card" id="lcControl">
      <h3 class="lc-section-title">Panel Kontrol Sesi Baru</h3>
      <form id="lcForm" class="lc-form-grid">
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcDevice">Nomor WA Penguji (connected) *</label></div>
          <select id="lcDevice" required></select>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcBotPhone">Nomor Bot Target (tujuan)</label></div>
          <input id="lcBotPhone" type="text" value="${escapeText(LC.defaultBotPhone || "")}" placeholder="Otomatis dari bot target akun Anda" />
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
        <div class="field-group">
          <div class="lc-label-row"><label for="lcRepeatCount">Jumlah Sesi (Perulangan)</label></div>
          <input id="lcRepeatCount" type="number" min="1" max="20" value="1" title="Menjalankan N sesi berturut-turut secara otomatis dengan data acak baru per sesi (jeda anti-ban 15-30 dtk antar-sesi)" />
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcTitle">Judul Pengaduan <small class="muted">(opsional)</small></label></div>
          <input id="lcTitle" type="text" maxlength="200" placeholder="Kosongkan untuk otomatis dari waktu sesi" />
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcReport">Isi Laporan / Pengaduan <small class="muted">(opsional)</small></label></div>
          <textarea id="lcReport" rows="3" placeholder="Opsional: kosongkan untuk respons otomatis sesuai alur bot"></textarea>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcNama">Nama Pengadu <small class="muted">(opsional)</small></label><span id="lcTagNama" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcNama" type="text" placeholder="Kosongkan untuk acak" /><button type="button" class="lc-mini-btn" data-reroll="nama">Acak ulang</button></div>
        </div>
        <div class="field-group">
          <div class="lc-label-row"><label for="lcTanggal">Tanggal Kejadian <small class="muted">(opsional)</small></label><span id="lcTagTanggal" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcTanggal" type="date" /><button type="button" class="lc-mini-btn" data-reroll="tanggal">Acak ulang</button></div>
        </div>
        <div class="lc-span-2 field-group">
          <div class="lc-label-row"><label for="lcAlamat">Alamat Kejadian <small class="muted">(opsional)</small></label><span id="lcTagAlamat" class="lc-label-badge random">acak</span></div>
          <div class="lc-input-actions"><input id="lcAlamat" type="text" placeholder="Kosongkan untuk acak" /><button type="button" class="lc-mini-btn" data-reroll="alamat">Acak ulang</button></div>
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
        <span id="lcHelp" class="muted" style="font-size:12px;">Hanya nomor WA penguji yang wajib. Form lainnya otomatis acak/default bila kosong.</span>
      </div>
    </div>

    <div class="lc-card">
      <h3 class="lc-section-title">Emulator Chat</h3>
      <div id="lcEmulatorGrid" class="lc-emulator-grid"></div>
    </div>

    <div class="lc-card">
      <div class="lc-label-row" style="margin-bottom:6px">
        <h3 class="lc-section-title" style="margin:0">Riwayat Sesi (<span id="lcHistoryTotalCount">0</span>)</h3>
        <button id="lcBtnRefreshHistory" type="button" class="lc-mini-btn">Muat ulang</button>
      </div>

      <div class="lc-history-toolbar">
        <div class="lc-toolbar-group">
          <input id="lcFilterQuery" type="text" placeholder="Cari judul / no tiket / bot..." class="lc-search-input" />
          <select id="lcFilterStatus">
            <option value="all">Semua Status</option>
            <option value="finished">Selesai (Finished)</option>
            <option value="running">Sedang Berjalan</option>
            <option value="failed">Gagal (Failed)</option>
            <option value="timeout">Timeout</option>
            <option value="stopped">Dihentikan</option>
          </select>
          <select id="lcFilterMode">
            <option value="all">Semua Mode</option>
            <option value="benar">Benar</option>
            <option value="typo">Typo</option>
            <option value="ngasal">Ngasal</option>
            <option value="celah">Cari Celah</option>
          </select>
          <select id="lcFilterSort">
            <option value="date_desc">Terbaru</option>
            <option value="date_asc">Terlama</option>
            <option value="turns_desc">Giliran Terbanyak</option>
            <option value="turns_asc">Giliran Tersedikit</option>
          </select>
          <select id="lcFilterPageSize">
            <option value="10">10 / hal</option>
            <option value="25">25 / hal</option>
            <option value="50">50 / hal</option>
          </select>
        </div>
      </div>

      <div class="table-wrapper">
        <table class="lc-history-table">
          <thead>
            <tr>
              <th>No</th><th>Tanggal</th><th>Judul</th><th>Mode</th>
              <th>Giliran</th><th>Status</th><th>Keterangan</th><th>Tiket</th><th>Aksi</th>
            </tr>
          </thead>
          <tbody id="lcHistoryBody"><tr><td colspan="9" class="muted">Memuat...</td></tr></tbody>
        </table>
      </div>

      <div class="lc-pagination-bar">
        <div class="lc-page-info">
          Menampilkan <span id="lcPageFrom">0</span>-<span id="lcPageTo">0</span> dari <span id="lcPageTotal">0</span> sesi
        </div>
        <div class="lc-page-controls">
          <button id="lcBtnPageFirst" type="button" class="lc-mini-btn" title="Halaman Pertama">&laquo;</button>
          <button id="lcBtnPagePrev" type="button" class="lc-mini-btn" title="Sebelumnya">&lsaquo;</button>
          <span id="lcPageCurrent">Halaman 1 dari 1</span>
          <button id="lcBtnPageNext" type="button" class="lc-mini-btn" title="Berikutnya">&rsaquo;</button>
          <button id="lcBtnPageLast" type="button" class="lc-mini-btn" title="Halaman Terakhir">&raquo;</button>
        </div>
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

async function loadSessions(resetPage = false) {
  if (resetPage) LC.pagination.page = 1;
  const p = LC.pagination;
  const qParams = new URLSearchParams({
    page: String(p.page),
    pageSize: String(p.pageSize),
    status: p.status,
    mode: p.mode,
    q: p.query,
    sort: p.sort,
  });
  try {
    const data = await apiLC("/api/livechat/sessions?" + qParams.toString());
    LC.sessions = data.sessions || [];
    p.total = data.total != null ? data.total : (data.sessions || []).length;
    p.totalPages = data.totalPages != null ? data.totalPages : 1;
    renderHistory();
    renderPaginationBar();
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
        ? `<img class="lc-asset-preview" src="${BASE_PATH_LC || ''}/api/livechat/assets/${a.id}/preview" alt="" loading="lazy" />`
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
  state.messages.forEach((m) => body.appendChild(buildBubble(m, s.id)));
  if (state.typing) body.appendChild(buildTypingIndicator());
  body.scrollTop = body.scrollHeight;
  return card;
}

function buildBubble(m, sessionId) {
  const kind = m.msgType || m.msg_type || "text";
  const dir = m.direction || "sent";
  const time = formatClock(m.sentAt || m.sent_at);
  const inner = renderBubbleContent(m, kind, sessionId);
  return h(`<div class="lc-bubble ${dir}">${inner}<span class="lc-time">${time}</span></div>`);
}
function renderBubbleContent(m, kind, sessionId) {
  const text = escapeText(m.body || m.bodyText || "");
  const media = parseMedia(m.mediaJson || m.media_json);
  if (kind === "image") {
    const file = media[0] && media[0].file;
    const src = file ? incomingMediaURL(sessionId, file) : "";
    const img = src
      ? `<img class="lc-media-thumb" src="${src}" alt="lampiran" onerror="this.style.display='none'" />`
      : "";
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

function formatKeterangan(s) {
  if (s.keterangan && s.keterangan.trim()) {
    return escapeText(s.keterangan.trim());
  }
  if (s.status === "finished") {
    return s.ticketNo ? `Diterima (#${escapeText(s.ticketNo)})` : "Diterima";
  }
  if (s.status === "failed") {
    return "Gagal: " + escapeText(s.endReason || "error");
  }
  if (s.status === "timeout") {
    return "Timeout: " + escapeText(s.endReason || "batas waktu");
  }
  if (s.status === "stopped") {
    return "Dihentikan: " + escapeText(s.endReason || "manual");
  }
  if (s.status === "running") {
    return "Sesi aktif";
  }
  return escapeText(s.endReason || "-");
}

function renderHistory() {
  const body = document.getElementById("lcHistoryBody");
  if (!body) return;
  const countEl = document.getElementById("lcHistoryTotalCount");
  if (countEl) countEl.textContent = String(LC.pagination.total);
  if (LC.sessions.length === 0) {
    body.innerHTML = `<tr><td colspan="9" class="muted" style="text-align:center;padding:18px;">Tidak ada sesi yang cocok dengan filter.</td></tr>`;
    return;
  }
  const startNo = (LC.pagination.page - 1) * LC.pagination.pageSize;
  body.innerHTML = LC.sessions
    .map((s, idx) => `
      <tr data-detail-id="${s.id}" class="${LC.activeDetailId === s.id ? "active-row" : ""}">
        <td>${startNo + idx + 1}</td>
        <td>${formatDate(s.createdAt)}</td>
        <td><strong>${escapeText(s.title || "")}</strong></td>
        <td><span class="muted">${escapeText(s.categoryMode || "")}</span></td>
        <td>${s.totalTurns || 0}</td>
        <td><span class="lc-chat-badge ${escapeText(s.status || "")}">${escapeText(s.status || "")}</span></td>
        <td class="lc-col-keterangan" title="${escapeText(s.keterangan || s.endReason || "")}">${formatKeterangan(s)}</td>
        <td>${escapeText(s.ticketNo || "-")}</td>
        <td style="white-space:nowrap">
          <button type="button" class="lc-btn-delete-row lc-mini-btn" data-del-id="${s.id}" title="Hapus sesi (soft delete)">🗑 Hapus</button>
        </td>
      </tr>
    `)
    .join("");
}

function renderPaginationBar() {
  const p = LC.pagination;
  const from = p.total === 0 ? 0 : (p.page - 1) * p.pageSize + 1;
  const to = Math.min(p.page * p.pageSize, p.total);
  const fromEl = document.getElementById("lcPageFrom");
  const toEl = document.getElementById("lcPageTo");
  const totalEl = document.getElementById("lcPageTotal");
  const curEl = document.getElementById("lcPageCurrent");
  if (fromEl) fromEl.textContent = String(from);
  if (toEl) toEl.textContent = String(to);
  if (totalEl) totalEl.textContent = String(p.total);
  if (curEl) curEl.textContent = `Halaman ${p.page} dari ${p.totalPages}`;

  const btnFirst = document.getElementById("lcBtnPageFirst");
  const btnPrev = document.getElementById("lcBtnPagePrev");
  const btnNext = document.getElementById("lcBtnPageNext");
  const btnLast = document.getElementById("lcBtnPageLast");
  if (btnFirst) btnFirst.disabled = p.page <= 1;
  if (btnPrev) btnPrev.disabled = p.page <= 1;
  if (btnNext) btnNext.disabled = p.page >= p.totalPages;
  if (btnLast) btnLast.disabled = p.page >= p.totalPages;
}

function renderDetailPanel(detail) {
  const panel = document.getElementById("lcDetailPanel");
  if (!panel) return;
  if (!detail) { panel.hidden = true; panel.innerHTML = ""; return; }
  LC.activeDetailData = detail;
  const s = detail.session;
  const isChat = LC.detailViewTab === "chat";

  const rowsHtml = (detail.turns || []).map((t) => `
    <tr>
      <td>${t.turnNo}</td>
      <td><span class="lc-label-badge ${t.questionKind === "konfirmasi" ? "manual" : "random"}">${escapeText(t.questionKind || "")}</span></td>
      <td>${escapeText(t.botQuestion || "")}</td>
      <td><strong>${escapeText(t.ourAnswer || "")}</strong></td>
      <td><span class="lc-chat-badge ${escapeText(t.matchStatus || "")}">${escapeText(t.matchStatus || "")}</span></td>
    </tr>
  `).join("");

  const bubblesHtml = (detail.messages || []).map((m) => {
    const kind = m.msgType || m.msg_type || "text";
    const dir = m.direction || "sent";
    const time = formatClock(m.sentAt || m.sent_at);
    const inner = renderBubbleContent(m, kind, s.id);
    return `<div class="lc-bubble ${dir}">${inner}<span class="lc-time">${time}</span></div>`;
  }).join("");

  panel.innerHTML = `
    <div class="lc-detail-header-row">
      <h3>Detail Sesi #${s.id} &mdash; ${escapeText(s.title || "Pengaduan")}</h3>
      <div style="display:flex;gap:8px;">
        <button id="lcBtnDeleteDetail" type="button" class="lc-mini-btn lc-btn-delete-row" data-del-id="${s.id}" title="Hapus sesi (soft delete)">🗑 Hapus Sesi</button>
        <button id="lcBtnCloseDetail" type="button" class="lc-mini-btn" style="color:var(--danger);font-weight:600;">✕ Tutup Detail</button>
      </div>
    </div>
    <div class="lc-detail-meta">
      <div>Status: <strong><span class="lc-chat-badge ${escapeText(s.status || "")}">${escapeText(s.status || "")}</span></strong></div>
      <div>Mode: <strong>${escapeText(s.categoryMode || "")}</strong></div>
      <div>Giliran: <strong>${s.totalTurns || 0}</strong></div>
      <div>Penguji (WA): <strong>${escapeText(s.waPhone || s.deviceUuid)}</strong></div>
      <div>Bot Target: <strong>${escapeText(s.botPhone || "-")}</strong></div>
      <div>Nomor Tiket: <strong>${escapeText(s.ticketNo || "-")}</strong></div>
      <div style="grid-column: span 3">Alasan Selesai: <strong>${escapeText(s.endReason || "-")}</strong></div>
      <div style="grid-column: span 3">Cuplikan Akhir: <em>${escapeText(s.lastChatExcerpt || "-")}</em></div>
      <div style="grid-column: span 3; display:flex; flex-direction:column; gap:4px; margin-top:6px; padding-top:6px; border-top:1px dashed var(--border);">
        <label style="font-size:12px; font-weight:600; color:var(--text);">Keterangan / Catatan Evaluasi Sesi:</label>
        <div style="display:flex; gap:8px;">
          <input id="lcInputDetailKeterangan" type="text" class="lc-search-input" style="flex:1" placeholder="Tuliskan catatan/keterangan evaluasi untuk sesi ini..." value="${escapeText(s.keterangan || "")}" />
          <button id="lcBtnSaveKeterangan" type="button" class="btn btn-secondary btn-sm" data-session-id="${s.id}">Simpan Keterangan</button>
        </div>
      </div>
    </div>

    <div class="lc-detail-tabs">
      <button type="button" class="lc-detail-tab-btn ${isChat ? "active" : ""}" data-detail-tab="chat">💬 Simulasi Chat WhatsApp (${(detail.messages || []).length} pesan)</button>
      <button type="button" class="lc-detail-tab-btn ${!isChat ? "active" : ""}" data-detail-tab="audit">📋 Log Audit QC per Giliran (${(detail.turns || []).length} giliran)</button>
    </div>

    ${isChat ? `
      <div class="lc-detail-chat-frame">
        <div class="lc-chat-header">
          <div class="lc-avatar">${escapeText(initials(s.waPhone))}</div>
          <div class="lc-meta">
            <span class="lc-phone">${escapeText(s.waPhone || s.deviceUuid)} &rarr; ${escapeText(s.botPhone || "Bot")}</span>
            <span class="lc-sub">${escapeText(s.title || "Pengaduan")} &bull; ${formatDate(s.createdAt)}</span>
          </div>
          <span class="lc-chat-badge ${escapeText(s.status || "")}">${escapeText(s.status || "")}</span>
        </div>
        <div class="lc-detail-chat-body">
          ${bubblesHtml || '<div class="muted" style="padding:24px;text-align:center;">Tidak ada riwayat pesan tersimpan.</div>'}
        </div>
      </div>
    ` : `
      <div class="table-wrapper">
        <table class="lc-history-table">
          <thead><tr><th>#</th><th>Jenis Pertanyaan</th><th>Pertanyaan Bot</th><th>Jawaban Kita</th><th>Status</th></tr></thead>
          <tbody>${rowsHtml || '<tr><td colspan="5" class="muted" style="text-align:center;padding:16px;">Belum ada log per giliran.</td></tr>'}</tbody>
        </table>
      </div>
    `}
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
    repeatCount: parseInt(document.getElementById("lcRepeatCount") ? document.getElementById("lcRepeatCount").value : "1", 10) || 1,
  };
  if (!body.deviceUuid) { toastLC("Pilih nomor WA penguji terlebih dahulu.", true); return; }

  try {
    const data = await apiLC("/api/livechat/sessions", { method: "POST", body });
    const count = body.repeatCount || 1;
    toastLC(count > 1 ? `Batch ${count} sesi dimulai secara berurutan. Pantau emulator di bawah.` : "Sesi dimulai. Pantau emulator di bawah.");
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

async function deleteSession(id) {
  if (!confirm(`Hapus sesi #${id} dari riwayat? (Soft delete: pesan & audit tetap aman tersimpan di database)`)) return;
  try {
    await apiLC(`/api/livechat/sessions/${id}`, { method: "DELETE" });
    toastLC(`Sesi #${id} berhasil dihapus.`);
    if (LC.activeDetailId === id) {
      LC.activeDetailId = 0;
      LC.activeDetailData = null;
      const p = document.getElementById("lcDetailPanel");
      if (p) { p.hidden = true; p.innerHTML = ""; }
    }
    LC.liveSessions.delete(id);
    renderEmulatorGrid();
    await loadSessions();
  } catch (err) {
    toastLC("Gagal menghapus sesi: " + err.message, true);
  }
}

async function saveSessionKeterangan(id, keterangan) {
  try {
    await apiLC(`/api/livechat/sessions/${id}/keterangan`, {
      method: "PATCH",
      body: { keterangan: keterangan.trim() },
    });
    toastLC("Keterangan sesi berhasil disimpan.");
    if (LC.activeDetailData && LC.activeDetailData.session && LC.activeDetailData.session.id === id) {
      LC.activeDetailData.session.keterangan = keterangan.trim();
    }
    await loadSessions();
  } catch (err) {
    toastLC("Gagal menyimpan keterangan: " + err.message, true);
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
      const sseUrl = (BASE_PATH_LC && !"/api/livechat/events".startsWith(BASE_PATH_LC)) ? (BASE_PATH_LC + "/api/livechat/events") : "/api/livechat/events";
      const res = await fetch(sseUrl, { credentials: "same-origin", signal: ctrl.signal });
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

  if (evt.type === "livechat_batch_info") {
    if (p.message) toastLC(p.message, p.status === "aborted");
    loadSessions();
    return;
  }

  if (evt.type === "livechat_session_created" && p.session) {
    const s = p.session;
    if (!LC.liveSessions.has(s.id)) {
      LC.liveSessions.set(s.id, {
        session: s,
        messages: [],
        turns: [],
        typing: false,
      });
      renderEmulatorGrid();
      loadSessions();
    }
    return;
  }

  const sid = p.sessionId;
  if (!sid) return;
  const state = LC.liveSessions.get(sid);
  if (evt.type === "livechat_message" && state) {
    const msg = {
      direction: p.direction,
      msgType: p.msgType,
      body: p.body,
      mediaJson: p.mediaJson,
      sentAt: new Date().toISOString(),
    };
    state.messages.push(msg);
    if (p.direction === "sent") state.typing = false;
    if (p.direction === "received") state.typing = false;
    appendMessageBubble(sid, msg);
    return;
  }
  if (evt.type === "livechat_turn" && state) {
    state.turns.push(p);
    state.typing = true; // anticipating the next bot reply
    updateTypingIndicator(sid, true);
    return;
  }
  if (evt.type === "livechat_status") {
    loadSessions();
    if (!state && p.status === "running") {
      loadSessionDetail(sid).then(() => renderEmulatorGrid());
      return;
    }
    if (state) {
      state.session.status = p.status;
      state.typing = false;
      renderEmulatorGrid();
      if (LC.activeDetailId === sid) openDetail(sid);
    }
    return;
  }
}

function appendMessageBubble(sid, msg) {
  const card = document.querySelector(`.lc-chat-card[data-session-id="${sid}"]`);
  if (!card) return;
  const body = card.querySelector(`.lc-chat-body[data-body-id="${sid}"]`);
  if (!body) return;
  const typing = body.querySelector(".lc-typing");
  if (typing) typing.remove();
  body.appendChild(buildBubble(msg, sid));
  body.scrollTop = body.scrollHeight;
}

function updateTypingIndicator(sid, isTyping) {
  const card = document.querySelector(`.lc-chat-card[data-session-id="${sid}"]`);
  if (!card) return;
  const body = card.querySelector(`.lc-chat-body[data-body-id="${sid}"]`);
  if (!body) return;
  const typing = body.querySelector(".lc-typing");
  if (isTyping) {
    if (!typing) {
      body.appendChild(buildTypingIndicator());
      body.scrollTop = body.scrollHeight;
    }
  } else {
    if (typing) typing.remove();
  }
}

// --- events binding ---
function bindEvents() {
  document.getElementById("lcBtnStart").addEventListener("click", startSession);
  document.getElementById("lcBtnDownloadExcel").addEventListener("click", async () => {
    try {
      const dlUrl = (BASE_PATH_LC && !"/api/livechat/excel/download".startsWith(BASE_PATH_LC)) ? (BASE_PATH_LC + "/api/livechat/excel/download") : "/api/livechat/excel/download";
      const res = await fetch(dlUrl, { credentials: "same-origin" });
      if (!res.ok) {
        const text = await res.text();
        let msg = `HTTP ${res.status}`;
        try { msg = JSON.parse(text).error || msg; } catch { /* raw */ }
        toastLC("Belum ada ringkasan live chat: " + msg, true);
        return;
      }
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "livechat.xlsx";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) {
      toastLC("Gagal mengunduh Excel: " + err.message, true);
    }
  });
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

  // History table row click or delete click.
  document.getElementById("lcHistoryBody").addEventListener("click", (e) => {
    const delBtn = e.target.closest(".lc-btn-delete-row");
    if (delBtn) {
      e.stopPropagation();
      const id = parseInt(delBtn.dataset.delId, 10);
      if (id) deleteSession(id);
      return;
    }
    const tr = e.target.closest("tr[data-detail-id]");
    if (!tr) return;
    openDetail(parseInt(tr.dataset.detailId, 10));
  });

  // Filter toolbar listeners
  const filterInput = document.getElementById("lcFilterQuery");
  let filterTimer = null;
  filterInput.addEventListener("input", () => {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => {
      LC.pagination.query = filterInput.value.trim();
      loadSessions(true);
    }, 350);
  });
  document.getElementById("lcFilterStatus").addEventListener("change", (e) => {
    LC.pagination.status = e.target.value;
    loadSessions(true);
  });
  document.getElementById("lcFilterMode").addEventListener("change", (e) => {
    LC.pagination.mode = e.target.value;
    loadSessions(true);
  });
  document.getElementById("lcFilterSort").addEventListener("change", (e) => {
    LC.pagination.sort = e.target.value;
    loadSessions(true);
  });
  document.getElementById("lcFilterPageSize").addEventListener("change", (e) => {
    LC.pagination.pageSize = parseInt(e.target.value, 10) || 10;
    loadSessions(true);
  });

  // Pagination navigation buttons
  document.getElementById("lcBtnPageFirst").addEventListener("click", () => {
    if (LC.pagination.page > 1) {
      LC.pagination.page = 1;
      loadSessions();
    }
  });
  document.getElementById("lcBtnPagePrev").addEventListener("click", () => {
    if (LC.pagination.page > 1) {
      LC.pagination.page--;
      loadSessions();
    }
  });
  document.getElementById("lcBtnPageNext").addEventListener("click", () => {
    if (LC.pagination.page < LC.pagination.totalPages) {
      LC.pagination.page++;
      loadSessions();
    }
  });
  document.getElementById("lcBtnPageLast").addEventListener("click", () => {
    if (LC.pagination.page < LC.pagination.totalPages) {
      LC.pagination.page = LC.pagination.totalPages;
      loadSessions();
    }
  });

  // Detail panel tabs, delete button, save keterangan & close button
  document.getElementById("lcDetailPanel").addEventListener("click", (e) => {
    const tabBtn = e.target.closest("[data-detail-tab]");
    if (tabBtn) {
      LC.detailViewTab = tabBtn.dataset.detailTab;
      renderDetailPanel(LC.activeDetailData);
      return;
    }
    const delBtn = e.target.closest("#lcBtnDeleteDetail");
    if (delBtn) {
      const id = parseInt(delBtn.dataset.delId, 10);
      if (id) deleteSession(id);
      return;
    }
    const saveKetBtn = e.target.closest("#lcBtnSaveKeterangan");
    if (saveKetBtn) {
      const id = parseInt(saveKetBtn.dataset.sessionId, 10);
      const input = document.getElementById("lcInputDetailKeterangan");
      const val = input ? input.value : "";
      if (id) saveSessionKeterangan(id, val);
      return;
    }
    const closeBtn = e.target.closest("#lcBtnCloseDetail");
    if (closeBtn) {
      LC.activeDetailId = 0;
      LC.activeDetailData = null;
      document.getElementById("lcDetailPanel").hidden = true;
      document.getElementById("lcDetailPanel").innerHTML = "";
    }
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
function incomingMediaURL(sessionId, file) {
  // Bot media is stored under data/users/{id}/livechat_media and served by the
  // session-scoped endpoint. Without a sessionId the URL is skipped (bubble
  // still renders text, image just stays hidden).
  if (!sessionId || !file) return "";
  const parts = String(file).split("/");
  const base = parts[parts.length - 1];
  if (!base) return "";
  return `/api/livechat/media/${sessionId}/${encodeURIComponent(base)}`;
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
