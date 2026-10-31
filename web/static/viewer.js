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
  Object.assign(saver, { on: false, host: false, asked: false, bytes: 0 });
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
  const video = videoStream(sock);

  ws.onopen = () => ws.send(JSON.stringify({ type: "connect", id, password }));
  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") {
      const data = ev.data;
      if (new Uint8Array(data, 0, 1)[0] === 0x56) { // 'V'
        video.push(data);
        return;
      }
      video.reset(); // back on JPEG tiles
      drawing = drawing.then(() => drawUpdate(sock, data)).catch((err) => console.error(err));
      return;
    }
    const msg = JSON.parse(ev.data);
    if (msg.type === "connected") {
      connected = true;
      session = { id, password, retryUntil: 0 };
      $("notice").textContent = "";
      $("session-label").textContent = `Connected to ${msg.id}`;
      checkBuild();
      $("connect-view").hidden = true;
      $("session-view").hidden = false;
      videoSupported.then((v) => {
        if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ type: "refresh", video: v }));
      });
      if (saver.on) sock.send(JSON.stringify({ type: "data_saver", ok: true }));
      if (realPointer) sock.send(JSON.stringify({ type: "real_pointer", ok: true }));
      checkMetered();
      canvas.focus();
    } else if (msg.type === "cursor") {
      canvas.style.cursor = msg.cursor;
    } else if (msg.type === "diagnostics") {
      showDiagnostics(msg.text || "");
    } else if (msg.type === "quality") {
      const q = $("quality"), want = msg.text || "auto";
      if (![...q.options].some((o) => o.value === want)) q.add(new Option(want, want));
      q.value = want;
      stats.scale = msg.scale || 100;
    } else if (msg.type === "data_saver") {
      saver.host = !!msg.ok;
    } else if (msg.type === "video") {
      stats.hostWhy = msg.text || "";
    } else if (msg.type === "displays") {
      showDisplays(msg.count || 1, msg.display || 0, !!msg.all);
    } else if (msg.type === "clipboard") {
      remoteClipboard(msg.text);
    } else if (msg.type === "error") {
      lastError = msg.error;
    }
  };
  ws.onclose = () => {
    video.reset();
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

// checkBuild shows the build the server runs, and says so when it changed
// since the last connection (the computer updated itself).
let build = "";
async function checkBuild() {
  try {
    const res = await fetch("/version", { cache: "no-store" });
    if (!res.ok) return;
    const now = (await res.text()).trim();
    if (build && now !== build) notice(`Updated to ${now}`, 8000);
    build = now;
    $("build").textContent = now;
  } catch (err) {
    console.error(err);
  }
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
  saver.bytes += buf.byteLength;
  stats.mode = "JPEG";
}

// H.264 video, decoded with WebCodecs. Browsers only offer WebCodecs on
// HTTPS or localhost (or an address the browser was told to trust); without
// it the host sends JPEG tiles instead.
const videoSupported = (async () => {
  if (!window.VideoDecoder) return false;
  try {
    return !!(await VideoDecoder.isConfigSupported({ codec: "avc1.42E01F", optimizeForLatency: true })).supported;
  } catch {
    return false;
  }
})();

// Why this browser cannot decode the video, or "" when it can.
let browserWhy = "";
videoSupported.then((ok) => {
  if (ok) return;
  browserWhy = !window.isSecureContext
    ? `this page is not trusted for video: add ${location.origin} to the browser's "Insecure origins treated as secure" flag and restart it`
    : "this browser cannot decode H.264";
});

function videoStream(sock) {
  let decoder = null;
  let codec = "";
  const pushed = new Map(); // seq -> when it arrived, to time decoding
  const ack = (seq) => {
    if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ type: "ack", seq }));
  };
  const reset = () => {
    if (decoder && decoder.state !== "closed") decoder.close();
    decoder = null;
    codec = "";
  };
  const open = (c) => {
    reset();
    decoder = new VideoDecoder({
      output: (frame) => {
        if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
          canvas.width = frame.displayWidth;
          canvas.height = frame.displayHeight;
          applyScale();
        }
        ctx2d.drawImage(frame, 0, 0);
        const t = pushed.get(frame.timestamp);
        if (t !== undefined) {
          pushed.delete(frame.timestamp);
          stats.decodeMs += performance.now() - t;
          stats.decoded++;
        }
        ack(frame.timestamp);
        frame.close();
        stats.frames++;
      },
      error: (err) => {
        // Fall back to JPEG for this session.
        console.error("video decoder:", err);
        decoder = null;
        if (sock.readyState === WebSocket.OPEN) sock.send(JSON.stringify({ type: "refresh", video: false }));
      },
    });
    decoder.configure({ codec: c, optimizeForLatency: true });
    codec = c;
  };
  return {
    reset,
    push(buf) {
      const dv = new DataView(buf);
      const seq = dv.getUint32(1, true);
      const key = (dv.getUint8(9) & 1) !== 0;
      const data = new Uint8Array(buf, 10);
      stats.bytes += buf.byteLength;
      saver.bytes += buf.byteLength;
      stats.mode = "H.264";
      if (key) {
        const c = codecFromSPS(data);
        if (c && (c !== codec || !decoder || decoder.state === "closed")) open(c);
      }
      if (!decoder || decoder.state !== "configured") {
        ack(seq); // nothing to draw it with yet; keep the host moving
        return;
      }
      if (pushed.size > 100) pushed.clear();
      pushed.set(seq, performance.now());
      stats.queue = Math.max(stats.queue, decoder.decodeQueueSize);
      decoder.decode(new EncodedVideoChunk({ type: key ? "key" : "delta", timestamp: seq, data }));
    },
  };
}

// codecFromSPS builds the WebCodecs codec string ("avc1.PPCCLL") from the
// sequence parameter set at the start of a key frame.
function codecFromSPS(data) {
  for (let i = 0; i + 7 < data.length; i++) {
    if (data[i] === 0 && data[i + 1] === 0 && data[i + 2] === 1 && (data[i + 3] & 0x1f) === 7) {
      const hex = (b) => b.toString(16).padStart(2, "0");
      return `avc1.${hex(data[i + 4])}${hex(data[i + 5])}${hex(data[i + 6])}`;
    }
  }
  return "";
}

// Frame rate and bandwidth, shown next to the session label.
// On JPEG the reason video is off is shown too, in full on hover.
const stats = { frames: 0, bytes: 0, mode: "", hostWhy: "", decodeMs: 0, decoded: 0, queue: 0, history: [], scale: 100 };
setInterval(() => {
  if (!ws) return;
  let mode = stats.mode;
  let why = "";
  if (mode === "JPEG") why = browserWhy || stats.hostWhy;
  if (why) mode += ` (no H.264: ${why.length > 60 ? why.slice(0, 57) + "…" : why})`;
  if (stats.mode === "H.264" && stats.scale < 100) mode += ` · ${stats.scale}%`;
  if (saver.host) mode += " · data saver";
  $("stats").textContent = `${stats.frames} fps · ${(stats.bytes * 8 / 1e6).toFixed(1)} Mbit/s · ${mode}`;
  $("saver").textContent = `Data saver: ${saver.on ? "on" : "off"} (${dataText(saver.bytes)} used)`;
  $("stats").title = why ? `Sending JPEG because ${why}` : "";
  const decode = stats.decoded ? ` · decode ${(stats.decodeMs / stats.decoded).toFixed(1)} ms · queue ${stats.queue}` : "";
  stats.history.push(`${new Date().toISOString().slice(11, 19)} ${$("stats").textContent}${decode}`);
  if (stats.history.length > 60) stats.history.shift();
  stats.decodeMs = stats.decoded = stats.queue = 0;
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

// Keys are taken at the window, not just the canvas, so typing still reaches
// the remote computer after a click on the toolbar or the black border. Only
// the toolbar's own menus keep their keys.
function forRemote(e) {
  const t = e.target;
  return ws && !$("session-view").hidden && !$("diag-dialog").open && !(t.closest && t.closest("header select, input"));
}

window.addEventListener("keydown", (e) => {
  if (!forRemote(e)) return;
  flushClipboard();
  if (e.code === "KeyV" && (e.ctrlKey || e.metaKey) && !e.altKey && !e.repeat) {
    swallowKeyUp = "KeyV";
    pasteTimer = setTimeout(() => { pasteTimer = null; sendV(); }, 200);
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  held.set(keyCode(e), e.key);
  sendInput({ kind: "keydown", key: e.key, code: keyCode(e) });
}, true);
window.addEventListener("keyup", (e) => {
  if (!forRemote(e)) return;
  e.preventDefault();
  e.stopPropagation();
  if (e.code === swallowKeyUp) { swallowKeyUp = null; return; }
  if (!held.delete(keyCode(e))) return; // its key-down went elsewhere
  sendInput({ kind: "keyup", key: e.key, code: keyCode(e) });
}, true);

// Key combinations the browser or the local OS would otherwise keep.
$("keys").addEventListener("change", (e) => {
  const codes = e.target.value.split("+").filter(Boolean);
  e.target.value = "";
  for (const code of codes) sendInput({ kind: "keydown", key: "", code });
  for (const code of codes.reverse()) sendInput({ kind: "keyup", key: "", code });
  canvas.focus();
});

// Full screen. In Chromium browsers (Chrome, Edge, Vivaldi, Brave) the
// keyboard is locked too, so browser shortcuts (Ctrl+T, Ctrl+W, F5...), Esc,
// Alt+Tab and the Windows key go to the remote computer; hold Esc to leave.
// Keyboard Lock only works on HTTPS or localhost, or when the browser is told
// to trust this address (see the README).
const canLock = !!(navigator.keyboard && navigator.keyboard.lock) && window.isSecureContext;
$("fullscreen").addEventListener("click", async () => {
  if (document.fullscreenElement) {
    await document.exitFullscreen();
    return;
  }
  if (canLock) navigator.keyboard.lock().catch(() => {});
  await $("session-view").requestFullscreen({ navigationUI: "hide" });
  canvas.focus();
  if (!canLock) notice("Browser shortcuts are not captured here: keyboard lock needs HTTPS (see README)", 6000);
});
document.addEventListener("fullscreenchange", () => {
  const full = !!document.fullscreenElement;
  $("fullscreen").textContent = full ? "Exit full screen" : "Full screen";
  if (!full && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
  releaseHeld();
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
// Option number count, when the host offers it, shows every monitor at once.
function showDisplays(count, current, all) {
  const sel = $("display");
  sel.hidden = count < 2;
  const choices = count + (all ? 1 : 0);
  if (sel.options.length !== choices) {
    sel.replaceChildren(...Array.from({ length: choices }, (_, i) => new Option(i < count ? `Display ${i + 1}` : "All displays", i)));
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
function notice(text, ms = 2000) {
  $("notice").textContent = text;
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => { $("notice").textContent = ""; }, ms);
}

// Diagnostics: this viewer's details and last minute of stats, followed by
// what the host sends back (its settings and recent log).
let diagLocal = "";
$("diag").addEventListener("click", async () => {
  const v = await videoSupported;
  diagLocal = [
    `viewer: ${navigator.userAgent}`,
    `page: ${location.origin}, secure context ${window.isSecureContext}, H.264 decode ${v}${browserWhy ? ` (${browserWhy})` : ""}`,
    `screen: canvas ${canvas.width}x${canvas.height}, window ${innerWidth}x${innerHeight} @${devicePixelRatio}x, ${actualSize ? "1:1" : "fit"}`,
    `quality: ${$("quality").value}, video at ${stats.scale}% of full resolution`,
    ...(linkResult ? [linkResult] : []),
    "--- last minute in this viewer ---",
    ...stats.history,
  ].join("\n");
  showDiagnostics(null);
  $("diag-dialog").showModal();
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "diagnostics" }));
});

function showDiagnostics(host) {
  const d = $("diag-dialog");
  if (host !== null && !d.open) return; // another viewer asked
  $("diag-text").value = `${diagLocal}\n\n--- remote computer ---\n${host ?? "(waiting for the remote computer…)"}`;
}

$("diag-copy").addEventListener("click", () => {
  const t = $("diag-text");
  t.select();
  document.execCommand("copy");
  $("diag-copy").textContent = "Copied";
  setTimeout(() => { $("diag-copy").textContent = "Copy"; }, 1500);
});
$("diag-close").addEventListener("click", () => $("diag-dialog").close());

// Data saver, for metered connections: the host sends at most 15 fps at
// half resolution while any viewer asks for it. The browser only knows a
// connection is metered on some phones (Chrome's Network Information API).
const saver = { on: false, host: false, asked: false, bytes: 0 };
function setSaver(on) {
  saver.on = on;
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "data_saver", ok: on }));
  $("saver-ask").hidden = true;
  if (!on) storeSet("saverOnMetered", "");
}
function checkMetered() {
  const c = navigator.connection;
  if (saver.on || saver.asked || !c || !(c.saveData || c.type === "cellular")) return;
  saver.asked = true;
  if (storeGet("saverOnMetered")) setSaver(true);
  else $("saver-ask").hidden = false;
}
if (navigator.connection) navigator.connection.addEventListener("change", () => { if (ws) checkMetered(); });
function storeGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
function storeSet(k, v) { try { localStorage.setItem(k, v); } catch { /* private window */ } }
function dataText(n) {
  if (n < 1e6) return `${(n / 1e3).toFixed(0)} KB`;
  if (n < 1e9) return `${(n / 1e6).toFixed(1)} MB`;
  return `${(n / 1e9).toFixed(2)} GB`;
}
$("saver").addEventListener("click", () => { setSaver(!saver.on); canvas.focus(); });
$("saver-yes").addEventListener("click", () => { setSaver(true); canvas.focus(); });
$("saver-always").addEventListener("click", () => { setSaver(true); storeSet("saverOnMetered", "1"); canvas.focus(); });
$("saver-no").addEventListener("click", () => { $("saver-ask").hidden = true; canvas.focus(); });

// The host draws its real pointer into the picture while a viewer asks;
// the canvas cursor stays as the pointer that moves without lag.
let realPointer = storeGet("realPointer") === "1";
function showRealPointer() { $("real-pointer").textContent = `Real pointer: ${realPointer ? "on" : "off"}`; }
showRealPointer();
$("real-pointer").addEventListener("click", () => {
  realPointer = !realPointer;
  storeSet("realPointer", realPointer ? "1" : "");
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "real_pointer", ok: realPointer }));
  showRealPointer();
  canvas.focus();
});

// Quality: the host scales video down to keep up; Auto picks the scale.
$("quality").addEventListener("change", () => {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "quality", text: $("quality").value }));
  canvas.focus();
});

// Link test: latency and download speed from the remote computer.
let linkResult = "";
$("diag-link").addEventListener("click", async () => {
  const b = $("diag-link");
  b.disabled = true;
  try {
    b.textContent = "Measuring ping…";
    const pings = [];
    for (let i = 0; i < 5; i++) {
      const t = performance.now();
      await (await fetch(`ping?${Date.now()}`, { cache: "no-store" })).text();
      pings.push(performance.now() - t);
    }
    pings.sort((x, y) => x - y);
    b.textContent = "Measuring speed…";
    const t = performance.now();
    const res = await fetch(`speedtest?mb=16&${Date.now()}`, { cache: "no-store" });
    const reader = res.body.getReader();
    let got = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      got += value.length;
    }
    const secs = (performance.now() - t) / 1000;
    linkResult = `link test: ping ${pings[0].toFixed(0)}-${pings[pings.length - 1].toFixed(0)} ms (median ${pings[2].toFixed(0)}), download ${(got * 8 / 1e6 / secs).toFixed(0)} Mbit/s (${(got / 1048576).toFixed(0)} MB in ${secs.toFixed(1)} s)`;
  } catch (err) {
    linkResult = `link test failed: ${err}`;
  } finally {
    b.disabled = false;
    b.textContent = "Test link";
  }
  const t = $("diag-text");
  t.value = `${linkResult}\n${t.value}`;
});
