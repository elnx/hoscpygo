"use strict";

const $ = (id) => document.getElementById(id);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
const hex2 = (v) => v.toString(16).padStart(2, "0");

const S = {
  devices: [],
  session: null,
  state: null,
  ws: null,
  wsRetry: 0,
  mode: null,
  srcW: 0,
  srcH: 0,
  deviceW: 0,
  deviceH: 0,
  highlightBounds: null,
  view: null,
  frameCanvas: document.createElement("canvas"),
  lastBitmap: null,
  lastFrameAt: 0,
  dragging: false,
  dragButton: "left",
  lastPointer: null,
  moveRAF: 0,
  layoutRoot: null,
  rotated: false,
  video: null,
  imageBusy: false,
  imagePending: null,
  toastTimer: 0,
};

/* ---------- small UI helpers ---------- */

function toast(msg, kind = "") {
  const el = document.createElement("div");
  el.className = "toast " + kind;
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), kind === "err" ? 10000 : 6000);
}

function setStatus(text, kind = "") {
  const el = $("status");
  el.textContent = text;
  el.className = kind;
}

async function api(path, opts) {
  const r = await fetch(path, Object.assign({ headers: { "Content-Type": "application/json" } }, opts));
  const ct = r.headers.get("content-type") || "";
  if (!r.ok) {
    let msg = r.status + " " + r.statusText;
    if (ct.includes("json")) {
      try { msg = (await r.json()).error || msg; } catch (e) { /* ignore */ }
    }
    throw new Error(msg);
  }
  if (ct.includes("application/json")) return r.json();
  if (ct.startsWith("image/")) return r.blob();
  return r.text();
}

/* ---------- devices / session ---------- */

async function loadDevices() {
  const sel = $("devices");
  const old = sel.value;
  sel.innerHTML = "";
  try {
    const data = await api("/api/devices");
    S.devices = data.devices || [];
    for (const d of S.devices) {
      const opt = document.createElement("option");
      opt.value = d.server + "\t" + d.sn;
      opt.textContent = d.sn + (d.remote ? "  @" + d.server : "");
      sel.appendChild(opt);
    }
    if (S.devices.length === 0) {
      const opt = document.createElement("option");
      opt.value = "";
      opt.textContent = "未发现设备（检查 USB/无线调试）";
      sel.appendChild(opt);
    }
    if (old) sel.value = old;
    if (data.errors && data.errors.length) {
      toast("部分 hdc 服务器不可用:\n" + data.errors.join("\n"), "err");
    }
  } catch (e) {
    toast("刷新设备失败: " + e.message, "err");
  }
}

async function connect() {
  const v = $("devices").value;
  if (!v) { toast("没有可用设备", "err"); return; }
  const [server, sn] = v.split("\t");
  setStatus("启动中…");
  try {
    const data = await api("/api/connect", {
      method: "POST",
      body: JSON.stringify({ sn, server, mode: $("connectMode").value }),
    });
    S.session = data.session;
    S.state = data.state;
    $("disconnect").disabled = false;
    $("connect").disabled = true;
    openWS();
  } catch (e) {
    setStatus("连接失败", "err");
    toast("连接失败: " + e.message, "err");
  }
}

async function disconnect() {
  if (!S.session) return;
  try { await api("/api/session/" + S.session + "/disconnect", { method: "POST" }); } catch (e) { /* ignore */ }
  closeWS();
  resetSession();
}

function resetSession() {
  S.session = null;
  S.state = null;
  S.mode = null;
  S.layoutRoot = null;
  S.lastBitmap = null;
  S.srcW = S.srcH = 0;
  S.deviceW = S.deviceH = 0;
  S.view = null;
  S.highlightBounds = null;
  $("tree").innerHTML = "";
  $("searchHits").innerHTML = "";
  $("tip").style.display = "";
  $("tip").textContent = "请选择设备并连接";
  $("screen").style.display = "";
  $("mseVideo").style.display = "none";
  $("connect").disabled = false;
  $("disconnect").disabled = true;
  setStatus("未连接");
  if (S.video) { S.video.reset(); S.video = null; }
  draw();
}

function openWS() {
  closeWS();
  if (!S.session) return;
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/ws?session=" + encodeURIComponent(S.session));
  ws.binaryType = "arraybuffer";
  S.ws = ws;
  ws.onopen = () => { S.wsRetry = 0; };
  ws.onmessage = (ev) => {
    if (typeof ev.data === "string") {
      try { handleJSON(JSON.parse(ev.data)); } catch (e) { console.warn(e); }
    } else {
      handleBinary(new Uint8Array(ev.data));
    }
  };
  ws.onclose = () => {
    if (S.session && S.ws === ws) {
      S.ws = null;
      S.wsRetry++;
      if (S.wsRetry > 8) {
        toast("连接已断开，请重新连接", "err");
        resetSession();
        return;
      }
      setStatus("连接断开，重连中…", "err");
      setTimeout(() => { if (S.session) openWS(); }, Math.min(5000, 800 * S.wsRetry));
    }
  };
  ws.onerror = () => { /* onclose handles */ };
}

function closeWS() {
  if (S.ws) {
    const ws = S.ws;
    S.ws = null;
    try { ws.close(); } catch (e) { /* ignore */ }
  }
}

function handleJSON(msg) {
  if (msg.type === "state") {
    S.state = msg;
    if (msg.mode) {
      const prev = S.mode;
      S.mode = msg.mode;
      if (prev !== msg.mode) {
        if (msg.mode === "image" && S.video) { S.video.reset(); S.video = null; }
        if (msg.mode === "video") { S.lastBitmap = null; }
        S.frameCanvas.width = 0; S.frameCanvas.height = 0;
      }
    }
    if (msg.width && msg.height) { S.deviceW = msg.width; S.deviceH = msg.height; }
    if (msg.state === "streaming") {
      ensureSize();
      setStatus((msg.mode === "video" ? "视频流" : "图片流") + " · " + msg.sn, "ok");
      $("tip").style.display = "none";
    } else if (msg.state === "error") {
      setStatus("错误", "err");
      $("tip").style.display = "";
      $("tip").textContent = msg.error || "采集失败";
      toast(msg.error || "采集失败", "err");
    } else if (msg.state === "starting") {
      setStatus("启动中…");
      $("tip").style.display = "";
      $("tip").textContent = msg.mode === "image" ? "视频不可用，回退图片流…" : "正在启动设备采集…";
    } else if (msg.state === "stopped") {
      setStatus("已停止");
    }
    draw();
  } else if (msg.type === "video-reset") {
    if (S.video) { S.video.reset(); S.video = null; }
    S.frameCanvas.width = 0;
    S.frameCanvas.height = 0;
    S.lastBitmap = null;
    draw();
  } else if (msg.type === "error") {
    toast(msg.message, "err");
  }
}

function handleBinary(bytes) {
  if (!bytes || bytes.length === 0) return;
  if (S.mode === "image") {
    handleImage(bytes);
  } else {
    if (!S.video) S.video = new VideoPipeline();
    S.video.feed(bytes);
  }
}

/* ---------- video decoding ---------- */

function splitNALs(bytes) {
  const out = [];
  let i = 0;
  const starts = [];
  while (i + 3 < bytes.length) {
    if (bytes[i] === 0 && bytes[i + 1] === 0 && bytes[i + 2] === 1) { starts.push([i, 3]); i += 3; continue; }
    if (i + 4 < bytes.length && bytes[i] === 0 && bytes[i + 1] === 0 && bytes[i + 2] === 0 && bytes[i + 3] === 1) { starts.push([i, 4]); i += 4; continue; }
    i++;
  }
  for (let k = 0; k < starts.length; k++) {
    const begin = starts[k][0] + starts[k][1];
    const end = k + 1 < starts.length ? starts[k + 1][0] : bytes.length;
    if (end > begin) out.push(bytes.subarray(begin, end));
  }
  return out;
}

function unescapeRBSP(nal) {
  const out = new Uint8Array(nal.length);
  let j = 0, zeros = 0;
  for (let i = 0; i < nal.length; i++) {
    const b = nal[i];
    if (zeros >= 2 && b === 3) { zeros = 0; continue; }
    out[j++] = b;
    zeros = b === 0 ? zeros + 1 : 0;
  }
  return out.subarray(0, j);
}

class BitReader {
  constructor(bytes) { this.b = bytes; this.pos = 0; }
  bit() {
    if (this.pos >= this.b.length * 8) throw new Error("SPS truncated");
    const v = (this.b[this.pos >> 3] >> (7 - (this.pos & 7))) & 1;
    this.pos++;
    return v;
  }
  bits(n) { let v = 0; for (let i = 0; i < n; i++) v = (v << 1) | this.bit(); return v; }
  ue() {
    let z = 0;
    while (this.bit() === 0) { z++; if (z > 31) throw new Error("bad ue"); }
    let v = 0;
    for (let i = 0; i < z; i++) v = (v << 1) | this.bit();
    return ((1 << z) - 1) + v;
  }
  se() { const v = this.ue(); return (v & 1) ? (v + 1) / 2 : -(v / 2); }
}

function skipScalingList(br, size) {
  let last = 8, next = 8;
  for (let j = 0; j < size; j++) {
    if (next !== 0) next = (last + br.se() + 256) % 256;
    if (next !== 0) last = next;
  }
}

function parseSPS(nal) {
  const rbsp = unescapeRBSP(nal.subarray(1));
  const br = new BitReader(rbsp);
  const profile = br.bits(8);
  const constraints = br.bits(8);
  const level = br.bits(8);
  br.ue(); // seq_parameter_set_id
  let chroma = 1;
  if ([100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135].includes(profile)) {
    chroma = br.ue();
    if (chroma === 3) br.bit(); // separate_colour_plane_flag
    br.ue(); // bit_depth_luma_minus8
    br.ue(); // bit_depth_chroma_minus8
    br.bit(); // qpprime_y_zero_transform_bypass_flag
    if (br.bit()) { // seq_scaling_matrix_present_flag
      const n = chroma !== 3 ? 8 : 12;
      for (let i = 0; i < n; i++) if (br.bit()) skipScalingList(br, i < 6 ? 16 : 64);
    }
  }
  br.ue(); // log2_max_frame_num_minus4
  const pocType = br.ue();
  if (pocType === 0) br.ue();
  else if (pocType === 1) {
    br.bit(); br.se(); br.se();
    const n = br.ue();
    for (let i = 0; i < n; i++) br.se();
  }
  br.ue(); // max_num_ref_frames
  br.bit(); // gaps_in_frame_num_value_allowed_flag
  const wMbs = br.ue() + 1;
  const hMap = br.ue() + 1;
  const frameMbsOnly = br.bit();
  if (!frameMbsOnly) br.bit();
  br.bit(); // direct_8x8_inference_flag
  let cl = 0, cr = 0, ct = 0, cb = 0;
  if (br.bit()) { cl = br.ue(); cr = br.ue(); ct = br.ue(); cb = br.ue(); }
  const subW = chroma === 3 ? 1 : 2;
  const subH = chroma === 1 ? 2 : 1;
  const cropUnitX = chroma === 0 ? 1 : subW;
  const cropUnitY = (chroma === 0 ? 1 : subH) * (2 - frameMbsOnly);
  const width = wMbs * 16 - (cl + cr) * cropUnitX;
  const height = (2 - frameMbsOnly) * hMap * 16 - (ct + cb) * cropUnitY;
  return { profile, constraints, level, width, height, chroma };
}

function buildAvcC(sps, pps) {
  const parts = [
    1, sps[1], sps[2], sps[3], 0xff, 0xe1,
    (sps.length >> 8) & 255, sps.length & 255, ...sps,
    1, (pps.length >> 8) & 255, pps.length & 255, ...pps,
  ];
  return new Uint8Array(parts);
}

function avccSample(nals) {
  let len = 0;
  for (const n of nals) len += 4 + n.length;
  const out = new Uint8Array(len);
  let o = 0;
  for (const n of nals) {
    out[o++] = (n.length >>> 24) & 255;
    out[o++] = (n.length >>> 16) & 255;
    out[o++] = (n.length >>> 8) & 255;
    out[o++] = n.length & 255;
    out.set(n, o);
    o += n.length;
  }
  return out;
}

function webcodecsMissingReason() {
  if (!window.isSecureContext) {
    const local = "http://127.0.0.1:" + (location.port || "8899");
    return "当前页面不是安全上下文（" + location.origin +
      "），WebCodecs 仅在 https、localhost 或 127.0.0.1 下可用；请改用 " + local + " 或 HTTPS 访问";
  }
  return "当前浏览器不提供 VideoDecoder，请使用新版 Chrome/Edge";
}

class VideoPipeline {
  constructor() {
    this.decoder = null;
    this.sps = null;
    this.pps = null;
    this.cfgKey = "";
    this.info = null;
    this.queue = [];
    this.failed = false;
    this.mse = null;
    this.usingMSE = false;
    this.rawQueue = [];
    this.needKey = true;
  }

  reset() {
    this.failed = false;
    this.usingMSE = false;
    this.queue = [];
    this.rawQueue = [];
    this.needKey = true;
    this.sps = this.pps = null;
    this.cfgKey = "";
    if (this.decoder) { try { this.decoder.close(); } catch (e) { /* ignore */ } this.decoder = null; }
    if (this.mse) { try { this.mse.destroy(); } catch (e) { /* ignore */ } this.mse = null; }
    $("mseVideo").style.display = "none";
    $("screen").style.display = "";
  }

  feed(bytes) {
    this.rawQueue.push(bytes);
    if (this.rawQueue.length > 90) this.rawQueue.shift();
    if (this.usingMSE) { this.feedMSE(bytes); return; }
    const nals = splitNALs(bytes);
    let sps = null, pps = null;
    for (const n of nals) {
      const t = n[0] & 0x1f;
      if (t === 7) sps = n;
      else if (t === 8) pps = n;
    }
    if (sps) this.sps = sps;
    if (pps) this.pps = pps;
    if (this.sps && this.pps) {
      const key = this.sps.length + ":" + this.sps[1] + ":" + this.sps[2] + ":" + this.sps[3] + ":" + this.pps.length;
      if (key !== this.cfgKey && !this.failed) this.configure(key);
    }
    const vcl = nals.filter((n) => { const t = n[0] & 0x1f; return t === 1 || t === 5; });
    if (!vcl.length) return;
    if (!this.decoder) {
      this.queue.push(vcl);
      if (this.queue.length > 90) this.queue.shift();
      return;
    }
    this.decode(vcl);
  }

  configure(key) {
    if (!("VideoDecoder" in window)) { this.fallback(webcodecsMissingReason()); return; }
    let info;
    try { info = parseSPS(this.sps); } catch (e) { this.fallback("SPS 解析失败: " + e.message); return; }
    if (!info.width || !info.height) { this.fallback("SPS 分辨率无效"); return; }
    this.info = info;
    const codec = "avc1." + hex2(info.profile) + hex2(info.constraints) + hex2(info.level);
    const desc = buildAvcC(this.sps, this.pps);
    if (this.decoder) { try { this.decoder.close(); } catch (e) { /* ignore */ } this.decoder = null; }
    const dec = new VideoDecoder({
      output: (frame) => this.onFrame(frame),
      error: (e) => this.fallback("解码错误: " + (e.message || e)),
    });
    try {
      dec.configure({
        codec,
        description: desc,
        codedWidth: info.width,
        codedHeight: info.height,
        optimizeForLatency: true,
      });
    } catch (e) {
      this.fallback("解码器配置失败: " + (e.message || e));
      return;
    }
    this.decoder = dec;
    this.cfgKey = key;
    this.needKey = true;
    S.frameCanvas.width = info.width;
    S.frameCanvas.height = info.height;
    setSourceSize(info.width, info.height);
    const q = this.queue.slice();
    this.queue = [];
    for (const vcl of q) this.decode(vcl);
  }

  decode(vcl) {
    if (!this.decoder) return;
    const key = vcl.some((n) => (n[0] & 0x1f) === 5);
    if (this.needKey && !key) return;
    if (key) this.needKey = false;
    try {
      this.decoder.decode(new EncodedVideoChunk({
        type: key ? "key" : "delta",
        timestamp: Math.round(performance.now() * 1000),
        data: avccSample(vcl),
      }));
    } catch (e) {
      this.fallback("decode 调用失败: " + (e.message || e));
    }
  }

  onFrame(frame) {
    try {
      const w = frame.displayWidth || frame.codedWidth;
      const h = frame.displayHeight || frame.codedHeight;
      if (S.frameCanvas.width !== w || S.frameCanvas.height !== h) {
        S.frameCanvas.width = w;
        S.frameCanvas.height = h;
        setSourceSize(w, h);
      }
      const ctx = S.frameCanvas.getContext("2d", { alpha: false });
      ctx.drawImage(frame, 0, 0, w, h);
      S.lastFrameAt = performance.now();
      draw();
    } finally {
      frame.close();
    }
  }

  fallback(reason) {
    if (this.failed) return;
    this.failed = true;
    console.warn("WebCodecs fallback:", reason, {
      origin: location.origin,
      secureContext: window.isSecureContext,
      videoDecoder: typeof window.VideoDecoder,
      ua: navigator.userAgent,
    });
    toast("WebCodecs 不可用，已回退 MSE：" + reason, "err");
    if (this.decoder) { try { this.decoder.close(); } catch (e) { /* ignore */ } this.decoder = null; }
    this.usingMSE = true;
    $("screen").style.display = "none";
    $("mseVideo").style.display = "block";
    if (typeof JMuxer === "undefined") {
      toast("MSE 回退失败：jmuxer 未加载", "err");
      return;
    }
    try {
      this.mse = new JMuxer({
        node: "mseVideo",
        mode: "video",
        flushingTime: 0,
        fps: 60,
        debug: false,
        onError: (e) => toast("MSE 错误: " + e, "err"),
      });
    } catch (e) {
      toast("MSE 初始化失败: " + e.message, "err");
      return;
    }
    const q = this.rawQueue.slice();
    for (const au of q) this.feedMSE(au);
  }

  feedMSE(bytes) {
    if (!this.mse) return;
    try { this.mse.feed({ video: bytes }); } catch (e) { /* ignore */ }
  }
}

function avccToAnnexB(nals) {
  let len = 0;
  for (const n of nals) len += 4 + n.length;
  const out = new Uint8Array(len);
  let o = 0;
  for (const n of nals) {
    out[o++] = 0; out[o++] = 0; out[o++] = 0; out[o++] = 1;
    out.set(n, o);
    o += n.length;
  }
  return out;
}

/* ---------- image path ---------- */

function handleImage(bytes) {
  S.imagePending = bytes;
  if (S.imageBusy) return;
  pumpImage();
}

async function pumpImage() {
  S.imageBusy = true;
  try {
    while (S.imagePending) {
      const buf = S.imagePending;
      S.imagePending = null;
      const blob = new Blob([buf], { type: "image/jpeg" });
      let bmp;
      try { bmp = await createImageBitmap(blob); } catch (e) { console.warn("jpeg decode", e); continue; }
      if (S.lastBitmap) S.lastBitmap.close();
      S.lastBitmap = bmp;
      setSourceSize(bmp.width, bmp.height);
      draw();
    }
  } finally {
    S.imageBusy = false;
  }
}

/* ---------- canvas / input mapping ---------- */

function setSourceSize(w, h) {
  if (!w || !h) return;
  if (w === S.srcW && h === S.srcH) return;
  S.srcW = w;
  S.srcH = h;
  draw();
}

function draw() {
  const canvas = $("screen");
  const wrap = $("screenWrap");
  if (S.mode === "video" && S.video && S.video.usingMSE) {
    updateMSERect();
    return;
  }
  canvas.style.display = "";
  $("mseVideo").style.display = "none";
  const rect = wrap.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const cssW = Math.max(1, rect.width);
  const cssH = Math.max(1, rect.height);
  if (canvas.width !== Math.round(cssW * dpr) || canvas.height !== Math.round(cssH * dpr)) {
    canvas.width = Math.round(cssW * dpr);
    canvas.height = Math.round(cssH * dpr);
    canvas.style.width = cssW + "px";
    canvas.style.height = cssH + "px";
  }
  const ctx = canvas.getContext("2d", { alpha: false });
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.fillStyle = "#05070a";
  ctx.fillRect(0, 0, cssW, cssH);
  if (!S.srcW || !S.srcH) return;
  const v = fitRect(S.srcW, S.srcH, cssW, cssH);
  S.view = { x: v.x, y: v.y, w: v.w, h: v.h, cssW, cssH };
  if (S.lastBitmap) {
    ctx.imageSmoothingEnabled = true;
    ctx.drawImage(S.lastBitmap, v.x, v.y, v.w, v.h);
  } else if (S.mode === "video" && S.frameCanvas.width && S.frameCanvas.height) {
    ctx.drawImage(S.frameCanvas, v.x, v.y, v.w, v.h);
  }
  updateHighlight();
}

function updateMSERect() {
  const video = $("mseVideo");
  const wrap = $("screenWrap");
  const wr = wrap.getBoundingClientRect();
  const vw = video.videoWidth || S.srcW;
  const vh = video.videoHeight || S.srcH;
  if (!vw || !vh) return;
  const v = fitRect(vw, vh, wr.width, wr.height);
  S.view = { x: v.x, y: v.y, w: v.w, h: v.h, cssW: wr.width, cssH: wr.height };
  updateHighlight();
}

function fitRect(sw, sh, cw, ch) {
  const scale = Math.min(cw / sw, ch / sh);
  const w = sw * scale, h = sh * scale;
  return { x: (cw - w) / 2, y: (ch - h) / 2, w, h, scale };
}

function devicePoint(ev) {
  if (!S.view) return null;
  const wrap = $("screenWrap").getBoundingClientRect();
  const x = ev.clientX - wrap.left - S.view.x;
  const y = ev.clientY - wrap.top - S.view.y;
  if (x < 0 || y < 0 || x > S.view.w || y > S.view.h) return null;
  const dw = S.deviceW || S.srcW;
  const dh = S.deviceH || S.srcH;
  return {
    x: Math.round(clamp(x / S.view.w * dw, 0, dw - 1)),
    y: Math.round(clamp(y / S.view.h * dh, 0, dh - 1)),
  };
}

function sendInput(msg) {
  if (S.ws && S.ws.readyState === WebSocket.OPEN) S.ws.send(JSON.stringify(msg));
}

function mouseMode() {
  return $("inputMode").value === "mouse";
}

function pointerButton(ev) {
  switch (ev.button) {
    case 0: return "left";
    case 1: return "middle";
    case 2: return "right";
  }
  return "";
}

$("screenWrap").addEventListener("pointerdown", (ev) => {
  if (!S.session) return;
  const mouse = mouseMode();
  const button = mouse ? pointerButton(ev) : (ev.button === 0 ? "left" : "");
  if (!button) return;
  const p = devicePoint(ev);
  if (!p) return;
  ev.preventDefault();
  S.lastPointer = p;
  S.dragging = true;
  S.dragButton = button;
  $("screenWrap").setPointerCapture(ev.pointerId);
  if (mouse) {
    sendInput({ type: "mouse", event: "down", button, x: p.x, y: p.y });
  } else {
    sendInput({ type: "touch", event: "down", x: p.x, y: p.y });
  }
});

$("screenWrap").addEventListener("pointermove", (ev) => {
  if (!S.dragging) return;
  const p = devicePoint(ev);
  if (!p) return;
  S.lastPointer = p;
  if (S.moveRAF) return;
  S.moveRAF = requestAnimationFrame(() => {
    S.moveRAF = 0;
    if (!S.dragging || !S.lastPointer) return;
    if (mouseMode()) {
      sendInput({ type: "mouse", event: "move", button: S.dragButton, x: S.lastPointer.x, y: S.lastPointer.y });
    } else {
      sendInput({ type: "touch", event: "move", x: S.lastPointer.x, y: S.lastPointer.y });
    }
  });
});

function endPointer(ev) {
  if (!S.dragging) return;
  const mouse = mouseMode();
  const p = devicePoint(ev) || S.lastPointer;
  S.dragging = false;
  if (S.moveRAF) {
    cancelAnimationFrame(S.moveRAF);
    S.moveRAF = 0;
  }
  if (!p) return;
  if (mouse) {
    sendInput({ type: "mouse", event: "up", button: S.dragButton, x: p.x, y: p.y });
  } else {
    sendInput({ type: "touch", event: "up", x: p.x, y: p.y });
  }
}

$("screenWrap").addEventListener("pointerup", endPointer);
$("screenWrap").addEventListener("pointercancel", endPointer);
$("screenWrap").addEventListener("contextmenu", (ev) => {
  ev.preventDefault();
  if (!S.session) return;
  if (mouseMode()) return; // right button is forwarded as a real mouse event
  sendInput({ type: "key", key: "Back" });
});

let wheelAt = 0;
$("screenWrap").addEventListener("wheel", (ev) => {
  if (!S.session) return;
  ev.preventDefault();
  const p = devicePoint(ev) || S.lastPointer;
  if (!p) return;
  const now = performance.now();
  if (now - wheelAt < 24) return;
  wheelAt = now;
  sendInput({ type: "wheel", dir: ev.deltaY < 0 ? "up" : "down", x: p.x, y: p.y });
}, { passive: false });

window.addEventListener("keydown", (ev) => {
  if (!S.session) return;
  const tag = (ev.target && ev.target.tagName) || "";
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || ev.target.isContentEditable) return;
  if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
  if (["Shift", "Control", "Alt", "Meta", "CapsLock", "NumLock", "Dead", "ContextMenu"].includes(ev.key)) return;
  const special = ["Backspace", "Enter", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Escape", "Tab", " "];
  if (special.includes(ev.key)) ev.preventDefault();
  sendInput({ type: "key", key: ev.key, shift: ev.shiftKey });
});

/* toolbar */
document.querySelectorAll("[data-key]").forEach((btn) => {
  btn.addEventListener("click", () => sendInput({ type: "key", key: btn.dataset.key }));
});
$("rotate").addEventListener("click", () => {
  S.rotated = !S.rotated;
  sendInput({ type: "rotate", horizontal: S.rotated });
});
$("fit").addEventListener("click", () => { draw(); });
$("sendText").addEventListener("click", sendText);
$("textInput").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter") { ev.preventDefault(); sendText(); }
});
function sendText() {
  const text = $("textInput").value;
  if (!text) return;
  sendInput({ type: "text", text });
  $("textInput").value = "";
}

/* ---------- layout tree ---------- */

let seeding = false;
async function seedImage() {
  if (!S.session || S.lastBitmap || seeding) return;
  seeding = true;
  try {
    const blob = await api("/api/session/" + S.session + "/screenshot");
    const bmp = await createImageBitmap(blob);
    if (S.lastBitmap) S.lastBitmap.close();
    S.lastBitmap = bmp;
    setSourceSize(bmp.width, bmp.height);
    draw();
  } catch (e) { /* the image stream will paint on the next change */ }
  seeding = false;
}

async function ensureSize() {
  if (!S.session || (S.deviceW && S.deviceH)) return;
  try {
    const sz = await api("/api/session/" + S.session + "/size");
    if (sz.width && sz.height) {
      S.deviceW = sz.width; S.deviceH = sz.height;
      draw();
    }
  } catch (e) { /* ignore */ }
}

async function refreshLayout() {
  if (!S.session) return;
  const tree = $("tree");
  tree.textContent = "加载中…";
  try {
    const raw = await api("/api/session/" + S.session + "/layout");
    S.layoutRoot = raw;
    renderTree(raw);
    renderHits(raw, $("search").value);
  } catch (e) {
    tree.textContent = "获取失败: " + e.message;
    toast("布局获取失败: " + e.message, "err");
  }
}

function renderTree(root) {
  const tree = $("tree");
  tree.innerHTML = "";
  const node = buildNode(root, 0);
  if (node) tree.appendChild(node);
}

function buildNode(n, depth) {
  if (!n) return null;
  const a = n.attributes || {};
  const type = a.type || "(node)";
  const text = a.text || a.description || a.id || "";
  const details = document.createElement("details");
  details.open = depth < 2;
  const summary = document.createElement("summary");
  const t = document.createElement("span");
  t.className = "t";
  t.textContent = type;
  summary.appendChild(t);
  if (text) {
    const x = document.createElement("span");
    x.className = "x";
    x.textContent = " · " + String(text).slice(0, 60);
    summary.appendChild(x);
  }
  summary.addEventListener("click", () => {
    highlightNode(n);
  });
  summary.addEventListener("dblclick", (ev) => {
    ev.preventDefault();
    tapNodeCenter(n);
  });
  details.appendChild(summary);
  for (const c of n.children || []) {
    const el = buildNode(c, depth + 1);
    if (el) details.appendChild(el);
  }
  return details;
}

function renderHits(root, q) {
  const box = $("searchHits");
  box.innerHTML = "";
  q = (q || "").trim().toLowerCase();
  if (!q) return;
  const hits = [];
  const walk = (n) => {
    if (!n || hits.length > 300) return;
    const a = n.attributes || {};
    const hay = ((a.type || "") + " " + (a.text || "") + " " + (a.description || "") + " " + (a.id || "")).toLowerCase();
    if (hay.includes(q)) hits.push(n);
    for (const c of n.children || []) walk(c);
  };
  walk(root);
  for (const n of hits) {
    const a = n.attributes || {};
    const div = document.createElement("div");
    div.className = "hit";
    div.textContent = (a.type || "?") + "  " + (a.text || a.description || a.id || "") + "  " + (a.bounds || "");
    div.addEventListener("click", () => highlightNode(n));
    div.addEventListener("dblclick", () => tapNodeCenter(n));
    box.appendChild(div);
  }
  if (!hits.length) box.textContent = "无匹配";
}

function parseBounds(n) {
  const a = n.attributes || {};
  const s = a.origBounds || a.bounds || "";
  const m = /\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]/.exec(s);
  if (!m) return null;
  return { l: +m[1], t: +m[2], r: +m[3], b: +m[4] };
}

function highlightNode(n) {
  const b = parseBounds(n);
  S.highlightBounds = b;
  updateHighlight();
}

function updateHighlight() {
  const el = $("highlight");
  const b = S.highlightBounds;
  if (!b || !S.view) { el.style.display = "none"; return; }
  const dw = S.deviceW || S.srcW;
  const dh = S.deviceH || S.srcH;
  if (!dw || !dh) { el.style.display = "none"; return; }
  const wrap = $("screenWrap").getBoundingClientRect();
  const x = S.view.x + (b.l / dw) * S.view.w;
  const y = S.view.y + (b.t / dh) * S.view.h;
  const w = ((b.r - b.l) / dw) * S.view.w;
  const h = ((b.b - b.t) / dh) * S.view.h;
  el.style.display = "block";
  el.style.left = (wrap.left + x) + "px";
  el.style.top = (wrap.top + y) + "px";
  el.style.width = Math.max(1, w) + "px";
  el.style.height = Math.max(1, h) + "px";
  el.style.position = "fixed";
}

function tapNodeCenter(n) {
  const b = parseBounds(n);
  if (!b) return;
  const x = Math.round((b.l + b.r) / 2);
  const y = Math.round((b.t + b.b) / 2);
  sendInput({ type: "touch", event: "down", x, y });
  setTimeout(() => sendInput({ type: "touch", event: "up", x, y }), 40);
}

/* ---------- screenshot ---------- */

$("screenshot").addEventListener("click", async () => {
  if (!S.session) return;
  try {
    const blob = await api("/api/session/" + S.session + "/screenshot");
    const url = URL.createObjectURL(blob);
    $("shotImg").src = url;
    $("shotDownload").href = url;
    $("shotDialog").showModal();
  } catch (e) {
    toast("截图失败: " + e.message, "err");
  }
});
$("shotClose").addEventListener("click", () => $("shotDialog").close());

/* ---------- settings ---------- */

async function loadConfig() {
  try {
    const c = await api("/api/config");
    $("cfgMode").value = c.mode;
    $("cfgScale").value = String(c.scale);
    $("cfgFps").value = c.frame_rate;
    $("cfgBitrate").value = c.bit_rate_mbps;
    $("cfgIFrame").value = c.i_frame_interval;
    $("cfgImageScale").value = c.image_scale;
    $("cfgRemotes").value = (c.remote_servers || []).join("\n");
    $("cfgHdc").value = c.hdc_path || "";
    $("cfgHTTPListen").value = c.http_listen || "";
    $("cfgHTTPSListen").value = c.https_listen || "";
    $("cfgOpen").value = String(c.auto_open_browser);
    $("cfgKeep").value = String(c.keep_agent);
    $("connectMode").value = c.mode;
  } catch (e) {
    toast("读取设置失败: " + e.message, "err");
  }
}

async function saveConfig() {
  const body = {
    mode: $("cfgMode").value,
    scale: +$("cfgScale").value,
    frame_rate: +$("cfgFps").value,
    bit_rate_mbps: +$("cfgBitrate").value,
    i_frame_interval: +$("cfgIFrame").value,
    image_scale: +$("cfgImageScale").value,
    remote_servers: $("cfgRemotes").value.split(/\s+/).filter(Boolean),
    hdc_path: $("cfgHdc").value.trim(),
    http_listen: $("cfgHTTPListen").value.trim(),
    https_listen: $("cfgHTTPSListen").value.trim(),
    auto_open_browser: $("cfgOpen").value === "true",
    keep_agent: $("cfgKeep").value === "true",
  };
  try {
    await api("/api/config", { method: "POST", body: JSON.stringify(body) });
    toast("设置已保存；新连接生效，监听/hdc 修改需重启", "ok");
    $("connectMode").value = body.mode;
    await loadDevices();
  } catch (e) {
    toast("保存失败: " + e.message, "err");
  }
}

/* ---------- tabs / wiring ---------- */

$("tabLayout").addEventListener("click", () => switchTab("layout"));
$("tabSettings").addEventListener("click", () => switchTab("settings"));
function switchTab(name) {
  $("tabLayout").classList.toggle("active", name === "layout");
  $("tabSettings").classList.toggle("active", name === "settings");
  $("layoutPanel").hidden = name !== "layout";
  $("settingsPanel").hidden = name !== "settings";
}

$("refreshDevices").addEventListener("click", loadDevices);
$("connect").addEventListener("click", connect);
$("disconnect").addEventListener("click", disconnect);
$("refreshLayout").addEventListener("click", refreshLayout);
$("search").addEventListener("input", () => { if (S.layoutRoot) renderHits(S.layoutRoot, $("search").value); });
$("saveConfig").addEventListener("click", saveConfig);
$("reloadConfig").addEventListener("click", loadConfig);

window.addEventListener("resize", () => draw());
window.addEventListener("beforeunload", () => closeWS());
$("mseVideo").addEventListener("loadedmetadata", () => { setSourceSize($("mseVideo").videoWidth, $("mseVideo").videoHeight); draw(); });

(async function init() {
  await loadDevices();
  await loadConfig();
  const params = new URLSearchParams(location.search);
  const qsid = params.get("session");
  const qsn = params.get("sn");
  if (qsid) {
    S.session = qsid;
    $("connect").disabled = true;
    $("disconnect").disabled = false;
    setStatus("连接中…");
    openWS();
  } else if (qsn) {
    const opt = [...$("devices").options].find((o) => o.value.endsWith("\t" + qsn));
    if (opt) {
      $("devices").value = opt.value;
      connect();
    }
  }
  draw();
})();
