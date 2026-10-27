"use strict";

const $ = (id) => document.getElementById(id);
const canvas = $("screen");
const ctx2d = canvas.getContext("2d");
let ws = null;
// While a session is open, a dropped connection (the host updating itself,
// a network blip) is retried for a minute before giving up.
let session = null; // { id, password, retryUntil }

$("connect-form").addEventListener("submit", (e) => {
  e.preventDefault();
  session = null;
  connect($("id").value.replace(/\s+/g, ""), $("password").value);
});
$("disconnect").addEventListener("click", () => {
  session = null;
  if (ws) ws.close();
});

function connect(id, password) {
  if (!session) setStatus("Connecting…");
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  ws = new WebSocket(`${proto}//${location.host}/ws/viewer`);
  const sock = ws;
  sock.binaryType = "arraybuffer";
  let connected = false;
  let lastError = "";
  // Updates must be drawn in order, since each one only paints what changed.
  let drawing = Promise.resolve();

  ws.onopen = () => ws.send(JSON.stringify({ type: "connect", id, password }));
  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") {
      const data = ev.data;
      drawing = drawing.then(() => drawUpdate(sock, data)).catch((err) => console.error(err));
      return;
    }
    const msg = JSON.parse(ev.data);
    if (msg.type === "connected") {
      connected = true;
      session = { id, password, retryUntil: 0 };
      $("notice").textContent = "";
      $("session-label").textContent = `Connected to ${msg.id}`;
      $("connect-view").hidden = true;
      $("session-view").hidden = false;
      ws.send(JSON.stringify({ type: "refresh" }));
      canvas.focus();
    } else if (msg.type === "cursor") {
      canvas.style.cursor = msg.cursor;
    } else if (msg.type === "displays") {
      showDisplays(msg.count || 1, msg.display || 0);
    } else if (msg.type === "clipboard") {
      remoteClipboard(msg.text);
    } else if (msg.type === "error") {
      lastError = msg.error;
    }
  };
  ws.onclose = () => {
    if (ws !== sock) return;
    ws = null;
    held.clear();
    if (session && retry()) return;
    session = null;
    $("session-view").hidden = true;
    $("connect-view").hidden = false;
    setStatus(lastError || (connected ? "Session ended." : "Could not reach the server."));
  };
}

// retry schedules another attempt to reopen the current session, keeping the
// last picture on screen, and reports whether it did.
function retry() {
  const now = Date.now();
  if (!session.retryUntil) session.retryUntil = now + 60_000;
  if (now > session.retryUntil) return false;
  $("notice").textContent = "Connection lost, reconnecting…";
  const { id, password } = session;
  setTimeout(() => { if (session && !ws) connect(id, password); }, 1000);
  return true;
}

// Screen updates: a header, then JPEG rectangles to paint over the last
// picture. See internal/stream for the layout.
async function drawUpdate(sock, buf) {
  const dv = new DataView(buf);
  if (dv.getUint8(0) !== 0x55) return; // 'U'
  const seq = dv.getUint32(1, true);
  const width = dv.getUint16(5, true), height = dv.getUint16(7, true);
  const count = dv.getUint16(9, true);
  const rects = [];
  let off = 11;
  for (let i = 0; i < count; i++) {
    const x = dv.getUint16(off, true), y = dv.getUint16(off + 2, true);
    const len = dv.getUint32(off + 8, true);
    const blob = new Blob([new Uint8Array(buf, off + 12, len)], { type: "image/jpeg" });
    rects.push({ x, y, blob });
    off += 12 + len;
  }
  const bitmaps = await Promise.all(rects.map((r) => createImageBitmap(r.blob)));
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
    applyScale();
  }
  rects.forEach((r, i) => {
    ctx2d.drawImage(bitmaps[i], r.x, r.y);
    bitmaps[i].close();
  });
  if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ type: "ack", seq }));
  stats.frames++;
  stats.bytes += buf.byteLength;
}

// Frame rate and bandwidth, shown next to the session label.
const stats = { frames: 0, bytes: 0 };
setInterval(() => {
  if (!ws) return;
  $("stats").textContent = `${stats.frames} fps · ${(stats.bytes * 8 / 1e6).toFixed(1)} Mbit/s`;
  stats.frames = 0;
  stats.bytes = 0;
}, 1000);

function setStatus(text) {
  $("status").textContent = text;
}

// Input forwarding. Coordinates are normalised to the host screen, taking the
// letterboxing from object-fit: contain into account.
function sendInput(input) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "input", input }));
}

function point(e) {
  const r = canvas.getBoundingClientRect();
  const scale = Math.min(r.width / canvas.width, r.height / canvas.height);
  const w = canvas.width * scale, h = canvas.height * scale;
  const x = (e.clientX - r.left - (r.width - w) / 2) / w;
  const y = (e.clientY - r.top - (r.height - h) / 2) / h;
  return { x: Math.min(Math.max(x, 0), 1), y: Math.min(Math.max(y, 0), 1) };
}

let moveQueued = null;
canvas.addEventListener("mousemove", (e) => {
  const first = moveQueued === null;
  moveQueued = point(e);
  if (first) requestAnimationFrame(() => { sendInput({ kind: "mousemove", ...moveQueued }); moveQueued = null; });
});
canvas.addEventListener("mousedown", (e) => { canvas.focus(); flushClipboard(); sendInput({ kind: "mousedown", button: e.button, ...point(e) }); });
canvas.addEventListener("mouseup", (e) => sendInput({ kind: "mouseup", button: e.button, ...point(e) }));
canvas.addEventListener("contextmenu", (e) => e.preventDefault());
canvas.addEventListener("wheel", (e) => { e.preventDefault(); sendInput({ kind: "wheel", deltaY: e.deltaY, ...point(e) }); }, { passive: false });

// Keyboard. On a Mac, Cmd is sent as Ctrl so Cmd+C / Cmd+V do what a Mac user
// expects on a Windows host.
const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
function keyCode(e) {
  if (isMac && e.code === "MetaLeft") return "ControlLeft";
  if (isMac && e.code === "MetaRight") return "ControlRight";
  return e.code;
}

// Ctrl+V (Cmd+V on a Mac) is let through to the browser so it fires a paste
// event with the local clipboard; that text goes to the host clipboard before
// the V key is sent, so the remote paste uses it.
let pasteTimer = null;
let swallowKeyUp = null;
function sendV() {
  sendInput({ kind: "keydown", key: "v", code: "KeyV" });
  sendInput({ kind: "keyup", key: "v", code: "KeyV" });
}

// Keys held down on the remote computer. If the viewer loses focus (say,
// Alt+Tab locally) the key-up never arrives, so they are released then.
const held = new Map();
function releaseHeld() {
  for (const [code, key] of held) sendInput({ kind: "keyup", key, code });
  held.clear();
}
window.addEventListener("blur", releaseHeld);
canvas.addEventListener("blur", releaseHeld);

canvas.addEventListener("keydown", (e) => {
  flushClipboard();
  if (e.code === "KeyV" && (e.ctrlKey || e.metaKey) && !e.altKey && !e.repeat) {
    swallowKeyUp = "KeyV";
    pasteTimer = setTimeout(() => { pasteTimer = null; sendV(); }, 200);
    return;
  }
  e.preventDefault();
  held.set(keyCode(e), e.key);
  sendInput({ kind: "keydown", key: e.key, code: keyCode(e) });
});
canvas.addEventListener("keyup", (e) => {
  e.preventDefault();
  if (e.code === swallowKeyUp) { swallowKeyUp = null; return; }
  held.delete(keyCode(e));
  sendInput({ kind: "keyup", key: e.key, code: keyCode(e) });
});

// Key combinations the browser or the local OS would otherwise keep.
$("keys").addEventListener("change", (e) => {
  const codes = e.target.value.split("+").filter(Boolean);
  e.target.value = "";
  for (const code of codes) sendInput({ kind: "keydown", key: "", code });
  for (const code of codes.reverse()) sendInput({ kind: "keyup", key: "", code });
  canvas.focus();
});

// Full screen. Where the browser allows it (HTTPS or localhost, Chrome and
// Edge) the keyboard is locked too, so Esc, Alt+Tab and the Windows key go to
// the remote computer; hold Esc to leave.
$("fullscreen").addEventListener("click", async () => {
  if (document.fullscreenElement) {
    await document.exitFullscreen();
    return;
  }
  await $("session-view").requestFullscreen();
  if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
  canvas.focus();
});
document.addEventListener("fullscreenchange", () => {
  $("fullscreen").textContent = document.fullscreenElement ? "Exit full screen" : "Full screen";
});

// Scaling: fit the window, or show remote pixels one to one with scrollbars.
let actualSize = false;
function applyScale() {
  $("screen-wrap").classList.toggle("actual", actualSize);
  $("scale").textContent = actualSize ? "Fit" : "1:1";
  canvas.style.width = actualSize ? `${canvas.width / devicePixelRatio}px` : "";
  canvas.style.height = actualSize ? `${canvas.height / devicePixelRatio}px` : "";
}
$("scale").addEventListener("click", () => {
  actualSize = !actualSize;
  applyScale();
  canvas.focus();
});

// Display switching on hosts with several monitors.
function showDisplays(count, current) {
  const sel = $("display");
  sel.hidden = count < 2;
  if (sel.options.length !== count) {
    sel.replaceChildren(...Array.from({ length: count }, (_, i) => new Option(`Display ${i + 1}`, i)));
  }
  sel.value = current;
}
$("display").addEventListener("change", (e) => {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "select_display", display: Number(e.target.value) }));
  canvas.focus();
});
document.addEventListener("paste", (e) => {
  if (pasteTimer === null) return;
  e.preventDefault();
  clearTimeout(pasteTimer);
  pasteTimer = null;
  const text = e.clipboardData.getData("text/plain");
  if (text && ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "clipboard", text }));
  sendV();
});

// Clipboard from the host. Browsers only let a page write the clipboard
// shortly after a click or key press, which is normally the remote Ctrl+C
// that changed it. If that window has passed, the text waits for the next
// click or key press in the viewer.
let pendingClipboard = null;
function remoteClipboard(text) {
  pendingClipboard = text;
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(() => {
      if (pendingClipboard === text) pendingClipboard = null;
      notice("Remote clipboard copied");
    }, () => flushClipboard());
  } else {
    flushClipboard();
  }
}

function flushClipboard() {
  if (pendingClipboard === null) return;
  const area = document.createElement("textarea");
  area.value = pendingClipboard;
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.appendChild(area);
  area.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { /* not allowed right now */ }
  area.remove();
  canvas.focus();
  if (ok) {
    pendingClipboard = null;
    notice("Remote clipboard copied");
  }
}

let noticeTimer = null;
function notice(text) {
  $("notice").textContent = text;
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => { $("notice").textContent = ""; }, 2000);
}
