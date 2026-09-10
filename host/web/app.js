/* OWRT Console — логика панели.
 *
 * Открывает WS к /ws/panel, шлёт команды, принимает вывод, рулит xterm.js,
 * историей команд (localStorage) и передачей файлов.
 */
(() => {
  "use strict";

  // ---------- конфиг ----------
  const WS_PATH = "/ws/panel";
  const HISTORY_KEY = "owrt.history.v1";
  const CHUNK_SIZE = 32 * 1024; // 32 КБ на чанк файла

  // ---------- состояние ----------
  let ws = null;
  let wsReady = false;
  let reconnectDelay = 1000;
  const MAX_DELAY = 10000;

  let currentClient = "";
  let clients = [];

  // буферы для приёма/отправки файлов
  let dlBuf = null;      // { path, size, chunks: [] }
  let dlInfo = null;     // { path, size, received }
  let ulState = null;    // { path, data: Uint8Array, offset }

  // ---------- xterm ----------
  const term = new Terminal({
    cursorBlink: true,
    fontFamily: "ui-monospace, Menlo, Consolas, monospace",
    fontSize: 14,
    theme: {
      background: "#0d0f12",
      foreground: "#d7dde5",
      cursor: "#4d6bfe",
      selectionBackground: "#2a3648",
    },
    scrollback: 5000,
    convertEol: true,
  });
  const fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  term.open(document.getElementById("terminal"));
  fitAddon.fit();
  window.addEventListener("resize", () => fitAddon.fit());

  // ---------- ввод с клавиатуры ----------
  let lineBuf = "";   // локальный буфер текущей строки (для истории)

  term.onData((data) => {
    if (!wsReady) return;

    // CTRL+C (0x03)
    if (data === "\u0003") {
      sendSignal("SIGINT");
      term.write("^C\r\n");
      lineBuf = "";
      return;
    }

    // Enter
    if (data === "\r") {
      const cmd = lineBuf;
      if (cmd.trim()) pushHistory(cmd);
      lineBuf = "";
      send({ type: "input", data: "\n" });
      term.write("\r\n");
      return;
    }

    // Backspace
    if (data === "\u007f") {
      if (lineBuf.length > 0) {
        lineBuf = lineBuf.slice(0, -1);
        // \b \b — стереть символ на экране
        term.write("\b \b");
      }
      return;
    }

    // Обычный ввод
    lineBuf += data;
    send({ type: "input", data: data });
    term.write(data); // локальный эхо; клиент PTY тоже эхоит — см. примечание в README
  });

  // ---------- WS ----------
  function connect() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${location.host}${WS_PATH}`;
    ws = new WebSocket(url);

    ws.onopen = () => {
      wsReady = true;
      reconnectDelay = 1000;
      setStatus("подключено к хосту", true);
      send({ type: "list_clients" });
    };

    ws.onclose = () => {
      wsReady = false;
      setStatus("нет связи с хостом, переподключение…", false);
      setTimeout(connect, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, MAX_DELAY);
    };

    ws.onerror = () => { /* onclose сам сработает */ };

    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch { return; }
      handle(m);
    };
  }

  function send(obj) {
    if (wsReady && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(obj));
    }
  }

  function sendSignal(sig) {
    send({ type: "signal", signal: sig });
  }

  // ---------- обработка сообщений ----------
  function handle(m) {
    switch (m.type) {
      case "clients":
        clients = m.clients || [];
        renderClients();
        break;

      case "client_status":
        if (m.client === currentClient) {
          setStatus(m.connected ? "клиент на связи" : "клиент отключён", !!m.connected);
        }
        break;

      case "output":
        if (m.client === currentClient) {
          term.write(m.data);
        }
        break;

      case "error":
        term.write(`\r\n\x1b[31m[ошибка] ${m.message}\x1b[0m\r\n`);
        break;

      // ---- файлы: скачивание с клиента ----
      case "file_download_start":
        dlInfo = { path: m.path, size: m.size, received: 0 };
        dlBuf = [];
        setFileStatus(`скачиваю ${m.path} (${m.size} байт)…`);
        break;

      case "file_download_chunk":
        if (dlBuf) {
          const bin = atob(m.data);
          const arr = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
          dlBuf.push(arr);
          if (dlInfo) dlInfo.received += arr.length;
          setFileStatus(`скачано ${dlInfo ? dlInfo.received : 0}/${dlInfo ? dlInfo.size : 0} байт`);
        }
        break;

      case "file_download_end":
        if (dlBuf && dlInfo) {
          finishDownload(dlInfo, dlBuf);
          dlBuf = null; dlInfo = null;
        }
        break;

      // ---- файлы: загрузка на клиент ----
      case "file_upload_start":
      case "file_upload_chunk":
      case "file_upload_end":
        // эхо от клиента — просто игнорируем, прогресс шлём локально
        break;

      default:
        // неизвестное — молча
        break;
    }
  }

  // ---------- клиенты ----------
  function renderClients() {
    const sel = document.getElementById("clientSelect");
    const prev = sel.value;
    sel.innerHTML = `<option value="">— нет —</option>`;
    clients.forEach((c) => {
      const opt = document.createElement("option");
      opt.value = c.name;
      opt.textContent = c.name;
      sel.appendChild(opt);
    });
    // восстановить выбор
    if (clients.some((c) => c.name === prev)) {
      sel.value = prev;
    } else if (currentClient && clients.some((c) => c.name === currentClient)) {
      sel.value = currentClient;
    }
  }

  document.getElementById("clientSelect").addEventListener("change", (e) => {
    currentClient = e.target.value;
    if (!currentClient) {
      setStatus("не выбран", false);
      return;
    }
    setStatus("выбираю…", false);
    send({ type: "select", client: currentClient });
    term.write(`\r\n\x1b[36m[панель] выбран клиент: ${currentClient}\x1b[0m\r\n`);
  });

  // ---------- статус ----------
  function setStatus(text, on) {
    document.getElementById("statusText").textContent = text;
    const dot = document.getElementById("statusDot");
    dot.classList.toggle("on", !!on);
    dot.classList.toggle("off", !on);
  }

  // ---------- кнопки ----------
  document.getElementById("btnCtrlC").addEventListener("click", () => {
    sendSignal("SIGINT");
    term.write("^C\r\n");
  });

  document.getElementById("btnClear").addEventListener("click", () => {
    term.clear();
  });

  document.getElementById("btnHistory").addEventListener("click", () => {
    document.getElementById("historyPanel").classList.toggle("hidden");
    document.getElementById("filesPanel").classList.add("hidden");
    renderHistory();
  });
  document.getElementById("btnHistoryClose").addEventListener("click", () => {
    document.getElementById("historyPanel").classList.add("hidden");
  });
  document.getElementById("btnHistoryClear").addEventListener("click", () => {
    localStorage.removeItem(HISTORY_KEY);
    renderHistory();
  });

  document.getElementById("btnFiles").addEventListener("click", () => {
    document.getElementById("filesPanel").classList.toggle("hidden");
    document.getElementById("historyPanel").classList.add("hidden");
  });
  document.getElementById("btnFilesClose").addEventListener("click", () => {
    document.getElementById("filesPanel").classList.add("hidden");
  });

  // ---------- история ----------
  function loadHistory() {
    try { return JSON.parse(localStorage.getItem(HISTORY_KEY) || "[]"); }
    catch { return []; }
  }
  function pushHistory(cmd) {
    const h = loadHistory();
    if (h[h.length - 1] !== cmd) h.push(cmd);
    if (h.length > 500) h.shift();
    localStorage.setItem(HISTORY_KEY, JSON.stringify(h));
  }
  function renderHistory() {
    const ul = document.getElementById("historyList");
    const h = loadHistory();
    ul.innerHTML = "";
    h.slice().reverse().forEach((cmd) => {
      const li = document.createElement("li");
      li.textContent = cmd;
      li.title = "клик — вставить в терминал";
      li.addEventListener("click", () => {
        // вставляем команду в терминал и в буфер
        lineBuf = cmd;
        send({ type: "input", data: cmd });
        term.write(cmd);
      });
      ul.appendChild(li);
    });
  }

  // ---------- файлы ----------
  function setFileStatus(text, cls) {
    const el = document.getElementById("fileStatus");
    el.textContent = text;
    el.className = "file-status" + (cls ? " " + cls : "");
  }

  // скачивание: клиент -> браузер
  document.getElementById("btnDownload").addEventListener("click", () => {
    const path = document.getElementById("dlPath").value.trim();
    if (!path) { setFileStatus("укажи путь", "err"); return; }
    if (!currentClient) { setFileStatus("клиент не выбран", "err"); return; }
    send({ type: "file_download", path });
    setFileStatus("запрос отправлен…");
  });

  function finishDownload(info, chunks) {
    const total = chunks.reduce((n, c) => n + c.length, 0);
    const out = new Uint8Array(total);
    let off = 0;
    for (const c of chunks) { out.set(c, off); off += c.length; }
    const blob = new Blob([out], { type: "application/octet-stream" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = info.path.split("/").pop() || "file.bin";
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    setFileStatus(`готово: ${a.download} (${total} байт)`, "ok");
  }

  // загрузка: браузер -> клиент
  document.getElementById("btnUpload").addEventListener("click", async () => {
    const path = document.getElementById("ulPath").value.trim();
    const fileInput = document.getElementById("ulFile");
    const file = fileInput.files && fileInput.files[0];
    if (!path) { setFileStatus("укажи путь на клиенте", "err"); return; }
    if (!file) { setFileStatus("выбери файл", "err"); return; }
    if (!currentClient) { setFileStatus("клиент не выбран", "err"); return; }

    const buf = new Uint8Array(await file.arrayBuffer());
    send({ type: "file_upload_start", path, size: buf.length });
    let seq = 0;
    for (let off = 0; off < buf.length; off += CHUNK_SIZE) {
      const chunk = buf.subarray(off, off + CHUNK_SIZE);
      send({ type: "file_upload_chunk", seq: seq++, data: b64encode(chunk) });
      setFileStatus(`загружено ${Math.min(off + CHUNK_SIZE, buf.length)}/${buf.length} байт`);
      // даём сокету продохнуть
      await new Promise((r) => setTimeout(r, 0));
    }
    send({ type: "file_upload_end" });
    setFileStatus(`готово: ${path} (${buf.length} байт)`, "ok");
  });

  function b64encode(u8) {
    let s = "";
    const CH = 0x8000;
    for (let i = 0; i < u8.length; i += CH) {
      s += String.fromCharCode.apply(null, u8.subarray(i, i + CH));
    }
    return btoa(s);
  }

  // ---------- старт ----------
  connect();
  renderHistory();
  term.focus();
})();
