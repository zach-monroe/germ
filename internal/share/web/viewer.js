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

  var ws = null, joined = false, ended = false, name = "", myName = "", retryUntil = 0;
  var you = 0, state = { tabs: [], requests: [] }, people = [], msgs = [], lastSeq = 0;
  var unread = 0, baseTitle = "germ · shared session", lastNoHelm = 0, composingAt = 0;
  // A random ID for this tab, kept across reconnects (and reloads), so a
  // guest the host let in isn't asked about again after a blip.
  var client = "";
  try { client = sessionStorage.getItem("germ-client") || ""; } catch (e) {}
  if (!client) {
    var r = nacl.randomBytes(16);
    client = Array.prototype.map.call(r, function (b) { return ("0" + b.toString(16)).slice(-2); }).join("");
    try { sessionStorage.setItem("germ-client", client); } catch (e) {}
  }

  function send(type, bytes) {
    if (!ws || ws.readyState !== 1) return;
    var nonce = nacl.randomBytes(24);
    var box = nacl.secretbox(concat(new Uint8Array([type]), bytes), nonce, key);
    ws.send(concat(nonce, box));
  }
  function sendJSON(type, obj) { send(type, enc.encode(JSON.stringify(obj))); }

  // Typing: only at the helm, and never mouse reports (they're for the
  // read-only tmux client drawing this page, not the program in the pane).
  term.onData(function (d) {
    if (/^\x1b\[(<\d|M|I$|O$)/.test(d)) return; // mouse and focus reports
    if (!atHelm()) {
      if (d === "\r" && state.viewing) { // Enter asks for the helm
        if (myRequest()) toast("Still waiting for the helm…");
        else { act({ op: "request", window: state.viewing }); toast("Asked for the helm…"); }
        return;
      }
      if (Date.now() - lastNoHelm > 4000) {
        lastNoHelm = Date.now();
        var t = tabOf(state.viewing);
        toast((t ? nameOf(t.helm) : "the host") + " has the helm here · Enter asks for it");
      }
      return;
    }
    send(0x44, enc.encode(d)); // 'D'
  });
  term.onResize(function (s) { sendJSON(0x52, { cols: s.cols, rows: s.rows }); }); // 'R'

  function connect() {
    var url = (location.protocol === "https:" ? "wss:" : "ws:") + "//" + location.host + G.prefix + "/ws/guest/" + encodeURIComponent(G.room);
    ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    ws.onopen = function () {
      sendJSON(0x48, { name: name, version: 3, kind: "web", client: client, cols: term.cols, rows: term.rows }); // 'H'
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
          you = w.you || 0;
          $("who").textContent = (w.host || "someone") + " · " + (w.session || "");
          baseTitle = "germ · " + (w.host || "shared session");
          updateTitle();
          term.reset();
          term.focus();
          renderHelm();
          break;
        }
        case 0x59: // 'Y' chat history
          msgs = JSON.parse(dec.decode(body)) || [];
          lastSeq = msgs.length ? msgs[msgs.length - 1].seq : 0;
          renderChat();
          break;
        case 0x43: { // 'C' chat message (seq 0: a notice just for us)
          var m = JSON.parse(dec.decode(body));
          if (!m.seq) { toast(m.text); break; }
          if (m.seq <= lastSeq) break;
          lastSeq = m.seq;
          msgs.push(m);
          if (msgs.length > 500) msgs.shift();
          if (!m.event && !(m.from === you && !m.host) && (document.hidden || $("chat").classList.contains("hidden"))) {
            unread++;
            updateTitle();
          }
          renderChat();
          break;
        }
        case 0x53: // 'S' state: tabs, the helm, requests
          state = JSON.parse(dec.decode(body));
          if (!state.tabs) state.tabs = [];
          if (!state.requests) state.requests = [];
          renderTabs();
          renderHelm();
          renderAsk();
          break;
        case 0x51: // 'Q' waiting for approval
          setMode("waiting", "wait");
          term.write("\x1b[2mWaiting for the host to let you in…\x1b[0m\r\n");
          break;
        case 0x44: term.write(body); break; // 'D'
        case 0x50: { // 'P' presence
          people = JSON.parse(dec.decode(body)) || [];
          $("people").textContent = people.map(function (p) {
            return short(p.name) + (p.host ? "★" : "") + (p.typing ? " ✎" : "");
          }).join(", ");
          $("here").textContent = "· " + people.length + " here";
          var typing = people.filter(function (p) { return p.composing && !(p.id === you && !p.host); })
            .map(function (p) { return short(p.name); });
          $("typing").textContent = typing.length ? typing.join(", ") + (typing.length > 1 ? " are" : " is") + " typing…" : "";
          renderTabs();
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

  // ---- chat and the helm ----
  function act(a) { sendJSON(0x41, a); } // 'A'
  // "alice@laptop" -> "alice", "alice@laptop (2)" -> "alice (2)".
  function short(n) { return String(n || "").replace(/@[^ ]*/, ""); }
  function tabOf(w) { for (var i = 0; i < state.tabs.length; i++) if (state.tabs[i].window === w) return state.tabs[i]; return null; }
  function nameOf(id) {
    if (!id) { for (var i = 0; i < people.length; i++) if (people[i].host) return short(people[i].name); return "the host"; }
    for (var j = 0; j < people.length; j++) if (people[j].id === id && !people[j].host) return short(people[j].name);
    return "someone";
  }
  function atHelm() { var t = tabOf(state.viewing); return !!t && you !== 0 && t.helm === you; }
  function myRequest() {
    for (var i = 0; i < state.requests.length; i++) {
      var r = state.requests[i];
      if (r.from === you && r.window === state.viewing) return r;
    }
    return null;
  }
  var toastTimer = null;
  function toast(text) {
    var t = $("toast");
    t.textContent = text;
    t.style.display = "block";
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.style.display = "none"; }, 4000);
  }
  function updateTitle() { document.title = (unread ? "(" + unread + ") " : "") + baseTitle; }
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden && !$("chat").classList.contains("hidden")) { unread = 0; updateTitle(); }
  });

  function renderHelm() {
    var b = $("helm"), r = myRequest();
    b.className = "";
    if (atHelm()) {
      b.textContent = "Hand back the helm"; b.className = "mine";
      setMode("⎈ at the helm", "type");
    } else if (r) {
      b.textContent = "Waiting for " + nameOf(r.approver) + "…"; b.className = "wait";
      setMode("watching", "watch");
    } else {
      b.textContent = "Request the helm";
      setMode("watching", "watch");
    }
  }
  $("helm").onclick = function () {
    if (!state.viewing) return;
    if (atHelm()) act({ op: "release", window: state.viewing });
    else if (!myRequest()) act({ op: "request", window: state.viewing });
    term.focus();
  };

  function renderTabs() {
    var box = $("tabs");
    box.textContent = "";
    state.tabs.forEach(function (t) {
      var b = document.createElement("button");
      b.className = t.window === state.viewing ? "cur" : "";
      b.title = "Look at this tab" + (t.window === state.current ? " (the host is here)" : "");
      b.appendChild(document.createTextNode(t.index + " " + (t.name || "") + (t.window === state.current ? " ●" : "")));
      if (t.helm) {
        var h = document.createElement("span");
        h.className = "helm";
        h.textContent = " ⎈" + (t.helm === you ? "you" : nameOf(t.helm));
        b.appendChild(h);
      }
      b.onclick = function () { act({ op: "view", window: t.window }); term.focus(); };
      box.appendChild(b);
    });
    if (!state.follow) {
      var f = document.createElement("button");
      f.className = "follow";
      f.textContent = "Follow the host";
      f.onclick = function () { act({ op: "follow" }); term.focus(); };
      box.appendChild(f);
    }
  }

  function renderAsk() {
    var mine = state.requests.filter(function (r) { return r.approver === you && you !== 0; });
    if (!mine.length) { $("ask").style.display = "none"; return; }
    var r = mine[0], t = tabOf(r.window);
    $("askText").textContent = "» " + short(r.name) + " wants the helm of tab " + (t ? t.index + (t.name ? " (" + t.name + ")" : "") : "") +
      (mine.length > 1 ? " (+" + (mine.length - 1) + ")" : "");
    $("ask").style.display = "block";
    $("askYes").onclick = function () { act({ op: "decide", request: r.id, allow: true }); };
    $("askNo").onclick = function () { act({ op: "decide", request: r.id, allow: false }); };
  }

  var nameColors = ["var(--blue)", "var(--purple)", "#7dcfff", "var(--green)", "var(--red)"];
  function linkify(el, text) {
    var re = /https?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)\]]/g, last = 0, m;
    while ((m = re.exec(text))) {
      el.appendChild(document.createTextNode(text.slice(last, m.index)));
      var a = document.createElement("a");
      a.href = m[0]; a.textContent = m[0]; a.target = "_blank"; a.rel = "noopener noreferrer";
      el.appendChild(a);
      last = m.index + m[0].length;
    }
    el.appendChild(document.createTextNode(text.slice(last)));
  }
  function renderChat() {
    var box = $("msgs");
    var atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
    box.textContent = "";
    var me = myName.toLowerCase(), prev = null;
    msgs.forEach(function (m) {
      var when = new Date(m.at);
      if (m.event) {
        var e = document.createElement("div");
        e.className = "event";
        e.textContent = "· " + m.text;
        box.appendChild(e);
        prev = null;
        return;
      }
      var div = document.createElement("div");
      div.className = "msg";
      if (!prev || prev.from !== m.from || prev.host !== m.host || m.at - prev.at > 300000) {
        var who = document.createElement("div");
        who.className = "who";
        who.style.color = m.host ? "var(--yellow)" : nameColors[m.from % nameColors.length];
        who.textContent = short(m.name) + (m.host ? " ★" : "");
        var ts = document.createElement("span");
        ts.textContent = " · " + ("0" + when.getHours()).slice(-2) + ":" + ("0" + when.getMinutes()).slice(-2);
        who.appendChild(ts);
        div.appendChild(who);
      }
      var tx = document.createElement("div");
      tx.className = "text";
      var low = m.text.toLowerCase();
      if (!(m.from === you && !m.host) && (low.indexOf("@here") >= 0 || (me && low.indexOf("@" + me) >= 0))) tx.className += " mention";
      linkify(tx, m.text);
      div.appendChild(tx);
      box.appendChild(div);
      prev = m;
    });
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  $("sayText").addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      var text = this.value.trim();
      if (text === "/helm") $("helm").onclick(); // ask for it, or hand it back
      else if (text) act({ op: "say", text: text.replace(/^\/\//, "/") });
      this.value = "";
      return;
    }
    if (e.key === "Escape") { term.focus(); return; }
    if (Date.now() - composingAt > 2000) { composingAt = Date.now(); act({ op: "composing" }); }
  });
  $("say").onsubmit = function (e) { e.preventDefault(); };
  $("chatBtn").onclick = function () {
    $("chat").classList.toggle("hidden");
    if (!$("chat").classList.contains("hidden")) { unread = 0; updateTitle(); $("sayText").focus(); }
    fit.fit();
  };
  if (window.innerWidth < 700) $("chat").classList.add("hidden");

  // Ask for a name first (remembered for next time).
  var saved = "";
  try { saved = localStorage.getItem("germ-name") || ""; } catch (e) {}
  $("name").value = saved;
  $("nameForm").onsubmit = function (e) {
    e.preventDefault();
    name = ($("name").value || "").trim() || "guest";
    try { localStorage.setItem("germ-name", name); } catch (e2) {}
    myName = name.split(/\s+/)[0]; // for @mentions
    name += " (browser)";
    joined = true;
    $("overlay").style.display = "none";
    fit.fit();
    connect();
    // Successful messages reset the reconnect window.
    setInterval(function () { if (ws && ws.readyState === 1) retryUntil = 0; }, 5000);
  };
})();
