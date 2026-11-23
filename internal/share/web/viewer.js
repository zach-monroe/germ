// germ's browser viewer: joins a shared session over the relay, decrypts
// with the key from the URL fragment (NaCl secretbox via tweetnacl, so it
// works on plain-http direct shares too), and renders the host's tmux in
// xterm.js. Can-type links send keystrokes; watch-only links can't.
(function () {
  "use strict";
  var G = window.GERM;
  var $ = function (id) { return document.getElementById(id); };
  var enc = new TextEncoder(), dec = new TextDecoder();

  function b64urlDecode(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s), out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  function concat(a, b) {
    var out = new Uint8Array(a.length + b.length);
    out.set(a, 0); out.set(b, a.length);
    return out;
  }

  var key = null;
  try { key = b64urlDecode(location.hash.slice(1)); } catch (e) {}
  var nativeURL = "germ://join/" + (location.protocol === "http:" ? "http:" : "") +
    location.host + G.prefix + "/" + G.room + location.hash;
  $("native").href = nativeURL;
  $("cmd").textContent = "germ join '" + location.href + "'";

  function setMode(text, cls) { var m = $("mode"); m.textContent = text; m.className = "badge " + cls; }
  if (!key || key.length !== 32) {
    $("join").innerHTML = "<h1>This link is incomplete</h1><p>It's missing its key (the part after #). " +
      "Ask for the whole link.</p>";
    setMode("bad link", "err");
    return;
  }

  // Install panel
  $("installBtn").onclick = function () { $("overlay").style.display = "flex"; $("join").style.display = "none"; $("install").style.display = "block"; };
  $("closeInstall").onclick = function () { $("install").style.display = "none"; if (!joined) $("join").style.display = "block"; else $("overlay").style.display = "none"; };

  var term = new Terminal({
    fontFamily: '"JetBrains Mono", "JetBrainsMono Nerd Font", ui-monospace, Menlo, Consolas, monospace',
    fontSize: 14, cursorBlink: true, scrollback: 5000, allowProposedApi: false,
    theme: { background: "#1a1b26", foreground: "#c0caf5", cursor: "#c0caf5", selectionBackground: "#33467c",
      black: "#15161e", red: "#f7768e", green: "#9ece6a", yellow: "#e0af68", blue: "#7aa2f7", magenta: "#bb9af7",
      cyan: "#7dcfff", white: "#a9b1d6", brightBlack: "#414868", brightRed: "#f7768e", brightGreen: "#9ece6a",
      brightYellow: "#e0af68", brightBlue: "#7aa2f7", brightMagenta: "#bb9af7", brightCyan: "#7dcfff", brightWhite: "#c0caf5" }
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("term"));
  fit.fit();
  window.addEventListener("resize", function () { fit.fit(); });

  var ws = null, readOnly = true, joined = false, ended = false, name = "", retryUntil = 0;

  function send(type, bytes) {
    if (!ws || ws.readyState !== 1) return;
    var nonce = nacl.randomBytes(24);
    var box = nacl.secretbox(concat(new Uint8Array([type]), bytes), nonce, key);
    ws.send(concat(nonce, box));
  }
  function sendJSON(type, obj) { send(type, enc.encode(JSON.stringify(obj))); }

  term.onData(function (d) { if (!readOnly) send(0x44, enc.encode(d)); });          // 'D'
  term.onBinary(function (d) {
    if (readOnly) return;
    var b = new Uint8Array(d.length);
    for (var i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 255;
    send(0x44, b);
  });
  term.onResize(function (s) { sendJSON(0x52, { cols: s.cols, rows: s.rows }); }); // 'R'

  function connect() {
    var url = (location.protocol === "https:" ? "wss:" : "ws:") + "//" + location.host + G.prefix + "/ws/guest/" + encodeURIComponent(G.room);
    ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    ws.onopen = function () {
      sendJSON(0x48, { name: name, version: 2, kind: "web", cols: term.cols, rows: term.rows }); // 'H'
    };
    ws.onmessage = function (ev) {
      var data = new Uint8Array(ev.data);
      if (data.length < 24 + 16) return;
      var msg = nacl.secretbox.open(data.subarray(24), data.subarray(0, 24), key);
      if (!msg || !msg.length) return;
      var body = msg.subarray(1);
      switch (msg[0]) {
        case 0x57: { // 'W' welcome
          var w = JSON.parse(dec.decode(body));
          readOnly = !!w.readonly;
          $("who").textContent = (w.host || "someone") + " · " + (w.session || "");
          setMode(readOnly ? "watching" : "can type", readOnly ? "watch" : "type");
          document.title = "germ · " + (w.host || "shared session");
          term.options.disableStdin = readOnly;
          term.reset();
          term.focus();
          break;
        }
        case 0x44: term.write(body); break; // 'D'
        case 0x50: { // 'P' presence
          var ps = JSON.parse(dec.decode(body));
          $("people").textContent = ps.map(function (p) {
            return p.name.split("@")[0] + (p.readonly ? " (watching)" : "") + (p.typing ? " ✎" : "");
          }).join(", ");
          break;
        }
        case 0x42: // 'B' bye
          ended = true;
          setMode("ended", "err");
          term.write("\r\n\x1b[1;31m" + dec.decode(body) + "\x1b[0m\r\n");
          break;
      }
    };
    ws.onclose = function () {
      if (ended) return;
      // Network blip, or the host's connection restarting: keep trying.
      if (!retryUntil) retryUntil = Date.now() + 90000;
      if (Date.now() > retryUntil) {
        setMode("disconnected", "err");
        term.write("\r\n\x1b[1;31mLost the connection to the shared session.\x1b[0m\r\n");
        return;
      }
      setMode("reconnecting…", "wait");
      setTimeout(connect, 2000);
    };
    ws.onerror = function () {};
  }

  // Ask for a name first (remembered for next time).
  var saved = "";
  try { saved = localStorage.getItem("germ-name") || ""; } catch (e) {}
  $("name").value = saved;
  $("nameForm").onsubmit = function (e) {
    e.preventDefault();
    name = ($("name").value || "").trim() || "guest";
    try { localStorage.setItem("germ-name", name); } catch (e2) {}
    name += " (browser)";
    joined = true;
    $("overlay").style.display = "none";
    fit.fit();
    connect();
    // Successful messages reset the reconnect window.
    setInterval(function () { if (ws && ws.readyState === 1) retryUntil = 0; }, 5000);
  };
})();
