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
  ws.binaryType = "blob";
  let connected = false;
  let lastError = "";

  ws.onopen = () => ws.send(JSON.stringify({ type: "connect", id, password }));
  ws.onmessage = async (ev) => {
    if (typeof ev.data !== "string") {
      const bmp = await createImageBitmap(ev.data);
      if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
        canvas.width = bmp.width;
        canvas.height = bmp.height;
      }
      ctx2d.drawImage(bmp, 0, 0);
      bmp.close();
      return;
    }
    const msg = JSON.parse(ev.data);
    if (msg.type === "connected") {
      connected = true;
      $("session-label").textContent = `Connected to ${msg.id}`;
      $("connect-view").hidden = true;
      $("session-view").hidden = false;
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
