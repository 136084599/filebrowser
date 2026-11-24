import { markRaw, reactive } from "vue";
import { sha256 } from "@noble/hashes/sha256";
import { bytesToHex } from "@noble/hashes/utils";
import { state } from "@/store";
import { getApiPath, getPublicApiPath } from "@/utils/url";
import { downloadManager } from "@/utils/downloadManager";
import { notifyDownloadComplete } from "@/utils/appNotifications";

export function safeRelativeParts(relativePath) {
  const parts = relativePath.split("/");
  if (!parts.length || parts.some(p => !p || p === "." || p === ".." || /[\\<>:"|?*]/.test(p) || [...p].some(char => char.charCodeAt(0) < 32) || /[. ]$/.test(p) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(p))) {
    throw new Error("Invalid local download path");
  }
  return parts;
}

/** Round-robin between selected folders so one large folder cannot starve others. */
export function interleaveFolders(files) {
  const groups = new Map();
  for (const file of files) {
    const key = file.relativePath.split("/")[0];
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(file);
  }
  const result = [];
  let index = 0;
  let remaining = true;
  while (remaining) {
    remaining = false;
    for (const group of groups.values()) if (index < group.length) { result.push(group.at(index)); remaining = true; }
    index++;
  }
  return result;
}

async function requestJSON(url, init = {}) {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  const value = await response.json();
  if (!response.ok) throw new Error(value.message || `HTTP ${response.status}`);
  return value;
}

function fileURL(plan, file) {
  return window.location.origin + getPublicApiPath(`transfers/${plan.ticket}/file/${file.id}`);
}

export const browserDownloadState = reactive({ entries: [], submitting: false, requested: 0 });

export function clearBrowserDownloads() {
  browserDownloadState.entries = [];
  browserDownloadState.submitting = false;
  browserDownloadState.requested = 0;
}

async function createDownloadPlan(items, generation) {
  const plan = await requestJSON(getApiPath("resources/batch-download"), {
    method: "POST", headers: { "Content-Type": "application/json", sessionId: state.sessionId },
    body: JSON.stringify({ selections: items.map(file => ({ source: file.source || state.req.source, path: file.path })) }),
  });
  if (generation !== downloadManager.generation) throw new DOMException("Cancelled", "AbortError");
  return plan;
}

/** Submit real browser downloads without loading file contents into JavaScript. */
export async function startBrowserDownloads(items) {
  if (browserDownloadState.submitting) return;
  clearBrowserDownloads();
  browserDownloadState.submitting = true;
  const generation = downloadManager.generation;
  try {
    const plan = await createDownloadPlan(items, generation);
    browserDownloadState.entries = interleaveFolders(plan.files).map(file => ({
      path: file.relativePath, name: file.relativePath.split("/").at(-1), url: fileURL(plan, file),
    }));
    for (const entry of browserDownloadState.entries) {
      if (generation !== downloadManager.generation) throw new DOMException("Cancelled", "AbortError");
      const link = document.createElement("a");
      link.href = entry.url;
      link.download = entry.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      // A click requests a task; browser policy decides whether to accept it.
      // Never report these requests as completed or integrity-verified files.
      browserDownloadState.requested++;
      await new Promise(resolve => setTimeout(resolve, 250));
    }
  } finally {
    if (generation === downloadManager.generation) browserDownloadState.submitting = false;
  }
}

async function directory(root, parts) {
  let target = root;
  for (const part of parts) target = await target.getDirectoryHandle(part, { create: true });
  return target;
}

export async function downloadBatchEntry(entry) {
  const { plan, file, root } = entry.batch;
  const controller = new AbortController();
  entry.abortController = controller;
  entry.status = "downloading";
  entry.loaded = 0;
  let writable;
  try {
    const parts = safeRelativeParts(file.relativePath);
    const parent = await directory(root, parts.slice(0, -1));
    const handle = await parent.getFileHandle(parts.at(-1), { create: true });
    writable = await handle.createWritable();
    let digest = sha256.create();
    const chunkSize = 8 * 1024 * 1024;
    while (entry.loaded < file.size) {
      const offset = entry.loaded;
      const end = Math.min(offset + chunkSize, file.size) - 1;
      const checkpoint = digest.clone();
      for (let attempt = 0; ; attempt++) {
        const chunkController = new AbortController();
        const abortChunk = () => chunkController.abort();
        controller.signal.addEventListener("abort", abortChunk);
        if (controller.signal.aborted) chunkController.abort();
        let idle = setTimeout(abortChunk, 60000);
        try {
          const response = await fetch(fileURL(plan, file), { headers: { Range: `bytes=${offset}-${end}` }, signal: chunkController.signal });
          if (response.status !== 206 || response.headers.get("Content-Range") !== `bytes ${offset}-${end}/${file.size}` || !response.body) {
            const error = new Error(`Invalid download range (HTTP ${response.status})`);
            error.retryable = response.status >= 500;
            await response.body?.cancel();
            throw error;
          }
          const reader = response.body.getReader();
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            clearTimeout(idle); idle = setTimeout(abortChunk, 60000);
            digest.update(value);
            await writable.write(value);
            entry.loaded += value.byteLength;
            entry.progress = entry.loaded / file.size * 100;
          }
          if (entry.loaded !== end + 1) throw new Error("Incomplete download chunk");
          break;
        } catch (error) {
          await writable.truncate(offset);
          await writable.seek(offset);
          digest = checkpoint.clone();
          entry.loaded = offset;
          if (controller.signal.aborted || attempt >= 2 || error.retryable === false) throw error;
          await new Promise(resolve => setTimeout(resolve, (attempt + 1) * 1000));
        } finally {
          clearTimeout(idle);
          controller.signal.removeEventListener("abort", abortChunk);
        }
      }
    }
    if (entry.loaded !== file.size) throw new Error("Incomplete download; retry this file");
    entry.verifyingIntegrity = true;
    const expected = await requestJSON(`${fileURL(plan, file)}?checksum=sha256`, { signal: controller.signal });
    if (expected.sha256 !== bytesToHex(digest.digest())) throw new Error("SHA-256 mismatch; the previous local file was kept unchanged");
    await writable.close();
    writable = null;
    entry.status = "completed";
    entry.progress = 100;
    notifyDownloadComplete(file.relativePath);
  } catch (error) {
    if (writable) await writable.abort().catch(() => {});
    entry.status = controller.signal.aborted ? "cancelled" : "error";
    entry.errorDetails = error.message;
  } finally { entry.abortController = null; entry.verifyingIntegrity = false; }
}

export async function startBatchDownload(items, { forceHelper = false, concurrency = 4 } = {}) {
  const generation = downloadManager.generation;
  // Picker must run directly from the button gesture, before network awaits.
  const canWrite = !forceHelper && window.isSecureContext && typeof window.showDirectoryPicker === "function";
  const root = canWrite ? await window.showDirectoryPicker({ mode: "readwrite" }) : null;
  const plan = await createDownloadPlan(items, generation);
  for (const dir of plan.directories) safeRelativeParts(dir);
  for (const file of plan.files) safeRelativeParts(file.relativePath);
  const names = new Set();
  for (const file of plan.files) {
    const key = file.relativePath.toLowerCase();
    if (names.has(key)) throw new Error("Selected files have conflicting local names");
    names.add(key);
  }
  if (!root) {
    // Native helper receives a scoped, expiring ticket; no account password or
    // unrestricted login/API token is exported to the task file.
    const task = { ...plan, apiBase: window.location.origin + getPublicApiPath("transfers/"), concurrency };
    const url = URL.createObjectURL(new Blob([JSON.stringify(task)], { type: "application/json" }));
    const link = document.createElement("a"); link.href = url; link.download = "FileBrowser-download.fbdownload"; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    return;
  }
  for (const dir of plan.directories) await directory(root, safeRelativeParts(dir));
  if (generation !== downloadManager.generation) throw new DOMException("Cancelled", "AbortError");
  const entries = interleaveFolders(plan.files).map(file => {
    const id = downloadManager.add({ name: file.relativePath, size: file.size });
    const entry = downloadManager.findById(id);
    entry.batch = markRaw({ plan, file, root });
    return entry;
  });
  let next = 0;
  const worker = async () => {
    while (next < entries.length) {
      const entry = entries[next++];
      if (entry.status !== "cancelled") await downloadBatchEntry(entry);
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, Math.min(8, concurrency)) }, worker));
}
