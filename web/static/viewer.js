"use strict";

const $ = (id) => document.getElementById(id);
const canvas = $("screen");
const ctx2d = canvas.getContext("2d");
let ws = null;

$("connect-form").addEventListener("submit", (e) => {
  e.preventDefault();
  connect($("id").value.replace(/\s+/g, ""), $("password").value);
});
$("disconnect").addEventListener("click", () => ws && ws.close());

function connect(id, password) {
  setStatus("Connecting…");
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
      $("session-label").textContent = `Connected to ${msg.id}`;
      $("connect-view").hidden = true;
      $("session-view").hidden = false;
      ws.send(JSON.stringify({ type: "refresh" }));
      canvas.focus();
    } else if (msg.type === "error") {
      lastError = msg.error;
    }
  };
  ws.onclose = () => {
    $("session-view").hidden = true;
    $("connect-view").hidden = false;
    setStatus(lastError || (connected ? "Session ended." : "Could not reach the server."));
    ws = null;
  };
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
canvas.addEventListener("mousedown", (e) => { canvas.focus(); sendInput({ kind: "mousedown", button: e.button, ...point(e) }); });
canvas.addEventListener("mouseup", (e) => sendInput({ kind: "mouseup", button: e.button, ...point(e) }));
canvas.addEventListener("contextmenu", (e) => e.preventDefault());
canvas.addEventListener("wheel", (e) => { e.preventDefault(); sendInput({ kind: "wheel", deltaY: e.deltaY, ...point(e) }); }, { passive: false });
canvas.addEventListener("keydown", (e) => { e.preventDefault(); sendInput({ kind: "keydown", key: e.key, code: e.code }); });
canvas.addEventListener("keyup", (e) => { e.preventDefault(); sendInput({ kind: "keyup", key: e.key, code: e.code }); });
