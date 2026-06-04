const ttlOptions = [
  ["10m", "10 minutes"],
  ["1h", "1 hour"],
  ["6h", "6 hours"],
  ["24h", "1 day"],
  ["3d", "3 days"],
  ["7d", "7 days"],
];

const state = {
  config: null,
  lastDeleteToken: "",
};

const $ = (selector) => document.querySelector(selector);
const basePath = document.querySelector('meta[name="qfs-base-path"]')?.content || "";

function apiPath(path) {
  return `${basePath}${path}`;
}

function setBusy(button, busy, text) {
  button.disabled = busy;
  if (busy) {
    button.dataset.label = button.textContent;
    button.textContent = text;
  } else if (button.dataset.label) {
    button.textContent = button.dataset.label;
  }
}

function fillTTL(select, selectedValue) {
  select.innerHTML = "";
  for (const [value, label] of ttlOptions) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    if (value === selectedValue) option.selected = true;
    select.append(option);
  }
  if (![...select.options].some((option) => option.value === selectedValue)) {
    const option = document.createElement("option");
    option.value = selectedValue;
    option.textContent = selectedValue;
    option.selected = true;
    select.prepend(option);
  }
}

async function loadConfig() {
  const res = await fetch(apiPath("/api/config"));
  if (!res.ok) throw new Error("Could not load server config");
  state.config = await res.json();
  $("#serverMeta").textContent = `Max ${state.config.maxUploadLabel} · default ${state.config.defaultTTL}`;
}

function showTab(tab) {
  const fileActive = tab === "file";
  $("#fileTab").classList.toggle("active", fileActive);
  $("#textTab").classList.toggle("active", !fileActive);
  $("#fileTab").setAttribute("aria-selected", String(fileActive));
  $("#textTab").setAttribute("aria-selected", String(!fileActive));
  $("#filePanel").hidden = !fileActive;
  $("#textPanel").hidden = fileActive;
  $("#filePanel").classList.toggle("active", fileActive);
  $("#textPanel").classList.toggle("active", !fileActive);
}

function wireDropZone() {
  const input = $("#fileInput");
  const drop = $("#dropZone");
  const name = $("#fileName");

  input.addEventListener("change", () => {
    name.textContent = input.files[0] ? input.files[0].name : "Select or drop a file";
  });

  for (const eventName of ["dragenter", "dragover"]) {
    drop.addEventListener(eventName, (event) => {
      event.preventDefault();
      drop.classList.add("dragging");
    });
  }
  for (const eventName of ["dragleave", "drop"]) {
    drop.addEventListener(eventName, (event) => {
      event.preventDefault();
      drop.classList.remove("dragging");
    });
  }
  drop.addEventListener("drop", (event) => {
    const files = event.dataTransfer.files;
    if (files.length > 0) {
      input.files = files;
      name.textContent = files[0].name;
    }
  });
}

function showResult(payload) {
  const template = $("#resultTemplate");
  const node = template.content.firstElementChild.cloneNode(true);
  node.querySelector(".result-name").textContent = `${payload.item.filename} · ${payload.item.sizeLabel}`;
  node.querySelector(".share-url").value = payload.shareUrl;
  node.querySelector(".open-link").href = payload.shareUrl;
  node.querySelector(".download-link").href = payload.item.downloadUrl;
  node.querySelector(".qr-img").src = payload.item.qrUrl;
  node.querySelector(".copy-link").addEventListener("click", () => copyText(payload.shareUrl));
  node.querySelector(".delete-button").addEventListener("click", async () => {
    if (!confirm("Delete this item now?")) return;
    const res = await fetch(apiPath(`/api/items/${payload.item.id}?token=${encodeURIComponent(payload.deleteToken)}`), {
      method: "DELETE",
    });
    if (!res.ok) {
      const err = await safeError(res);
      alert(err);
      return;
    }
    $("#result").hidden = true;
  });

  const result = $("#result");
  result.replaceChildren(node);
  result.hidden = false;
}

async function uploadFile(event) {
  event.preventDefault();
  const input = $("#fileInput");
  if (!input.files[0]) {
    alert("Select a file first");
    return;
  }
  const submit = $("#fileSubmit");
  setBusy(submit, true, "Uploading");

  const data = new FormData();
  data.append("file", input.files[0]);
  data.append("ttl", $("#fileTTL").value);

  try {
    const res = await fetch(apiPath("/api/upload"), { method: "POST", body: data });
    if (!res.ok) throw new Error(await safeError(res));
    showResult(await res.json());
  } catch (error) {
    alert(error.message);
  } finally {
    setBusy(submit, false);
  }
}

async function uploadText(event) {
  event.preventDefault();
  const text = $("#textInput").value;
  if (!text.trim()) {
    alert("Paste text first");
    return;
  }
  const submit = $("#textSubmit");
  setBusy(submit, true, "Sharing");

  try {
    const res = await fetch(apiPath("/api/text"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: $("#textName").value,
        text,
        ttl: $("#textTTL").value,
      }),
    });
    if (!res.ok) throw new Error(await safeError(res));
    showResult(await res.json());
  } catch (error) {
    alert(error.message);
  } finally {
    setBusy(submit, false);
  }
}

async function renderShare(id) {
  $("#homeView").hidden = true;
  $("#shareView").hidden = false;
  $("#shareStatus").textContent = "Loading";

  const res = await fetch(apiPath(`/api/items/${id}`));
  if (!res.ok) {
    $("#shareStatus").textContent = await safeError(res);
    return;
  }
  const item = await res.json();
  $("#shareStatus").textContent = "";

  const card = document.createElement("article");
  card.className = "share-card";
  card.innerHTML = `
    <div class="share-head">
      <div>
        <h1></h1>
        <div class="share-meta">
          <span class="pill"></span>
          <span class="pill"></span>
          <span class="pill"></span>
        </div>
        <div class="actions">
          <a class="button download-action">Download</a>
          <button class="secondary copy-action" type="button">Copy Link</button>
        </div>
      </div>
      <img class="qr-img" alt="QR code">
    </div>
  `;
  card.querySelector("h1").textContent = item.filename;
  const pills = card.querySelectorAll(".pill");
  pills[0].textContent = item.sizeLabel;
  pills[1].textContent = item.previewable ? item.previewFormat : "download";
  pills[2].textContent = expiresLabel(item.secondsRemaining);
  card.querySelector(".download-action").href = item.downloadUrl;
  card.querySelector(".copy-action").addEventListener("click", () => copyText(location.href));
  card.querySelector(".qr-img").src = item.qrUrl;

  $("#shareContent").replaceChildren(card);
  if (item.previewable) {
    await renderPreview(card, item);
  }
}

async function renderPreview(card, item) {
  const preview = document.createElement("section");
  preview.className = "preview";
  preview.innerHTML = `
    <div class="preview-toolbar">
      <h2>Preview</h2>
      <button class="secondary copy-text" type="button">Copy Text</button>
    </div>
    <div class="preview-body"></div>
  `;
  card.append(preview);

  const res = await fetch(apiPath(`/api/items/${item.id}/preview`));
  if (!res.ok) {
    preview.querySelector(".preview-body").textContent = await safeError(res);
    return;
  }
  const data = await res.json();
  preview.querySelector(".copy-text").addEventListener("click", () => copyText(data.text));
  if (data.format === "markdown") {
    const rendered = document.createElement("div");
    rendered.className = "markdown-preview";
    rendered.innerHTML = data.html || "";
    preview.querySelector(".preview-body").replaceChildren(rendered);
  } else {
    const pre = document.createElement("pre");
    pre.textContent = data.text;
    preview.querySelector(".preview-body").replaceChildren(pre);
  }
  if (data.truncated) {
    const note = document.createElement("p");
    note.className = "muted";
    note.textContent = "Preview truncated";
    preview.append(note);
  }
}

function expiresLabel(seconds) {
  if (seconds <= 60) return "expires soon";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m left`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h left`;
  return `${Math.floor(seconds / 86400)}d left`;
}

async function copyText(value) {
  await navigator.clipboard.writeText(value);
}

async function safeError(res) {
  try {
    const body = await res.json();
    return body.error || res.statusText;
  } catch {
    return res.statusText;
  }
}

async function init() {
  await loadConfig();
  fillTTL($("#fileTTL"), state.config.defaultTTLValue);
  fillTTL($("#textTTL"), state.config.defaultTTLValue);
  wireDropZone();
  $("#fileTab").addEventListener("click", () => showTab("file"));
  $("#textTab").addEventListener("click", () => showTab("text"));
  $("#fileForm").addEventListener("submit", uploadFile);
  $("#textForm").addEventListener("submit", uploadText);

  const escapedBasePath = basePath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const sharePattern = new RegExp(`^${escapedBasePath}/s/([A-Za-z0-9_-]+)$`);
  const match = location.pathname.match(sharePattern);
  if (match) {
    await renderShare(match[1]);
  }
}

init().catch((error) => {
  document.body.innerHTML = `<main class="shell"><p class="error-text">${error.message}</p></main>`;
});
