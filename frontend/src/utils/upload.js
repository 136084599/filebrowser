import { reactive } from "vue";
import { resourcesApi } from "@/api";
import { mutations, state } from "@/store";
import { getters } from "@/store/getters";
import { notify } from "@/notify";
import i18n from "@/i18n";
import { prepareUploadIntegrity, validateChunkAcknowledgement } from "@/utils/uploadIntegrity";
import {
  notifyUploadComplete,
  notifyUploadError,
} from "@/utils/appNotifications";

/**
 * Upload session token for isolating concurrent uploads to the same path.
 * Prefers crypto.randomUUID(); falls back for older browsers / non-secure contexts.
 * @returns {string}
 */
export function newUploadSessionId() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    // RFC 4122 version 4 bits
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  return `upload-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

/**
 * Accumulates every directory entry from a FileSystemDirectoryReader.
 *
 * Chromium and Safari cap DirectoryReader.readEntries() at roughly one batch
 * (often 100 entries) per call — callers must loop until a batch is empty.
 *
 * @param {FileSystemDirectoryReader} reader
 * @param {string} [debugLabel] optional path/name for diagnostic logs
 * @returns {Promise<FileSystemEntry[]>}
 */
export async function readAllDirectoryEntries(reader, debugLabel = "") {
  /** @type {FileSystemEntry[]} */
  const all = [];
  let nonemptyBatches = 0;

  /** @type {FileSystemEntry[]} */
  let batch = await new Promise((resolve, reject) => {
    reader.readEntries(resolve, reject);
  });

  while (batch.length > 0) {
    nonemptyBatches++;
    all.push(...batch);
    batch = await new Promise((resolve, reject) => {
      reader.readEntries(resolve, reject);
    });
  }

  if (nonemptyBatches > 1 || all.length > 100) {
    const suffix =
      debugLabel !== ""
        ? ` (${debugLabel})`
        : "";
    console.info(
      `[FileBrowser upload] directory listing${suffix}: ${nonemptyBatches} readEntries batches — collected ${all.length} entries (multiple batches indicate a directory with > ~100 immediate children)`
    );
  }

  return all;
}

// Directory listings report disk usage (rounded up to 4 KiB blocks) unless the source
// sets useLogicalSize, so a complete remote file matches either its exact size or the
// block-rounded one. Anything else (e.g. an empty leftover of a failed upload) differs.
const DISK_BLOCK_SIZE = 4096;

export function isSameSize(remoteSize, localSize) {
  return (
    remoteSize === localSize ||
    remoteSize === Math.ceil(localSize / DISK_BLOCK_SIZE) * DISK_BLOCK_SIZE
  );
}

// "name.ext" -> "name_01.ext" (suffix goes before the extension).
export function numberedName(name, n) {
  const dot = name.lastIndexOf(".");
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : "";
  return `${stem}_${String(n).padStart(2, "0")}${ext}`;
}

class UploadManager {
  constructor() {
    this.queue = reactive([]);
    this.activeUploads = 0;
    this.nextId = 0;
    this.overwriteAll = null; // null: ask, true: overwrite, false: skip
    this.isPausedForConflict = false;
    this.isOverallPaused = false;
    this.onConflict = () => {}; // Callback for UI
    this.hadActiveUploads = false; // Track if we've had active uploads
    this.conflictingFolder = null; // Track the folder name that caused conflict
    this.pendingItems = null; // Store pending items during conflict resolution
    this.probedDirs = new Set(); // Track directories that were probed/created during conflict check
    this.progressTimeouts = new Map(); // Track progress timeouts per upload ID
    this.lastUploadActivityTime = null;
    this.PROGRESS_TIMEOUT_MS = 120000;
    this.VERIFY_TIMEOUT_MS = 30 * 60 * 1000;
    this.MAX_REQUEST_RETRIES = 5;
    this.control = reactive({ cancelling: false });
    this.probedDirectorySessions = new Map();
    this.cleanupDirectories = new Map();
  }

  setOnConflict(handler) {
    this.onConflict = handler;
  }

  /**
   * Decides what to do with each item that already exists at the destination, so
   * a repeated upload of a folder only sends what is missing or incomplete:
   * - same size (see isSameSize), or a numbered copy of it with the same size: skipped
   * - existing file is empty (leftover of a failed upload): replaced
   * - any other size: uploaded under the next free numbered name (name_01.ext, ...)
   *   so the existing file is never overwritten
   * @returns {Promise<{items: object[], skipped: number, renamed: number}>}
   */
  async filterExistingItems(basePath, items) {
    const dirOf = (item) => {
      const relativePath = item.relativePath || item.file.name;
      const slash = relativePath.lastIndexOf("/");
      return slash === -1 ? basePath : `${basePath}${relativePath.slice(0, slash + 1)}`;
    };

    const dirPaths = [...new Set(Array.from(items).map(dirOf))];
    const listings = new Map();
    const listBatchSize = 5;
    for (let i = 0; i < dirPaths.length; i += listBatchSize) {
      await Promise.all(
        dirPaths.slice(i, i + listBatchSize).map(async (dirPath) => {
          listings.set(
            dirPath,
            await resourcesApi.listDirectoryEntries(state.req.source, dirPath)
          );
        })
      );
    }

    let skipped = 0;
    let renamed = 0;
    const remaining = [];
    for (const item of items) {
      const dirPath = dirOf(item);
      const listing = listings.get(dirPath);
      const name = item.file.name;
      const existing = listing?.get(name);
      if (!existing) {
        remaining.push(item);
        continue;
      }
      const isFile = existing.type !== "directory";
      if (isFile && isSameSize(existing.size, item.file.size)) {
        skipped++;
        continue;
      }
      if (isFile && existing.size === 0) {
        remaining.push({ ...item, overwriteExisting: true });
        continue;
      }

      // Different content under the same name: keep it and use a numbered name.
      let n = 1;
      let alreadyUploaded = false;
      while (listing.has(numberedName(name, n))) {
        const copy = listing.get(numberedName(name, n));
        if (copy.type !== "directory" && isSameSize(copy.size, item.file.size)) {
          alreadyUploaded = true;
          break;
        }
        n++;
      }
      if (alreadyUploaded) {
        skipped++;
        continue;
      }
      const newName = numberedName(name, n);
      // Reserve the name so two items of this batch can't pick the same one.
      listing.set(newName, { size: item.file.size, type: "file" });
      const relativePath = item.relativePath || name;
      const slash = relativePath.lastIndexOf("/");
      renamed++;
      remaining.push({
        ...item,
        relativePath: `${relativePath.slice(0, slash + 1)}${newName}`,
        uploadName: newName,
      });
    }
    return { items: remaining, skipped, renamed };
  }

  resolveConflict(basePath, items, resolution) {
    if (resolution === true) {
      // User chose overwrite - set the flag and add with overwrite=true
      this.overwriteAll = true;
      void this.add(basePath, items, true);
    } else if (resolution?.rename) {
      // User chose rename - continue with renamed items
      this.conflictingFolder = null;
      void this.add(basePath, this.pendingItems, false);
    } else if (resolution?.skip) {
      // User chose skip - only upload what is missing or incomplete
      const pending = this.pendingItems;
      this.conflictingFolder = null;
      void this.add(basePath, pending, false, true);
    } else {
      // User cancelled
      this.overwriteAll = null;
      this.conflictingFolder = null;
      this.pendingItems = null;
    }
  }

  async add(basePath, items, overwrite = false, skipExisting = false) {
    // Handle undefined/null basePath
    if (!basePath) {
      basePath = "/";
    }
    if (basePath.slice(-1) !== "/") {
      basePath += "/";
    }

    // Pre-upload conflict check for top-level directories
    // Skip probing if overwrite is already true or overwriteAll is set
    if (this.overwriteAll === null && !overwrite && !skipExisting) {
      const topLevelDirs = new Set();
      for (const item of items) {
        if (item.relativePath?.includes('/')) {
          topLevelDirs.add(item.relativePath.split('/')[0]);
        }
      }

      if (topLevelDirs.size > 0) {
        // First try using state.req.items if available (regular uploads)
        const existingItems = new Set((state.req.items || []).map((i) => i.name));
        let conflictingDirs = [...topLevelDirs].filter(dir => existingItems.has(dir));
        let probedDirs = new Set();

        // If state.req.items is not available (upload shares), probe the server
        if (existingItems.size === 0 && topLevelDirs.size > 0) {
          // Probe each top-level directory to check for conflicts
          const probeResults = await Promise.all(
            [...topLevelDirs].map(async (dirName) => {
              try {
                const testPath = `${basePath}${dirName}`;
                const sessionId = newUploadSessionId();
                const headers = { 'X-File-Upload-Session': sessionId };
                if (getters.isShare()) {
                  await resourcesApi.postPublic(state.shareInfo?.hash, testPath, new Blob([]), false, undefined, headers, true);
                } else {
                  await resourcesApi.post(state.req?.source, testPath, new Blob([]), false, undefined, headers, true);
                }
                // No conflict - directory was created successfully
                // Mark it so we can skip it later in the queue
                this.probedDirectorySessions.set(dirName, sessionId);
                return { dirName, conflict: false, probed: true };
              } catch (err) {
                // 409 means conflict
                if (err?.response?.status === 409) {
                  return { dirName, conflict: true, probed: false };
                }
                // Other errors are actual failures, treat as no conflict for now
                return { dirName, conflict: false, probed: false };
              }
            })
          );
          conflictingDirs = probeResults
            .filter(result => result.conflict)
            .map(result => result.dirName);
          // Track which directories we successfully probed (created)
          probedDirs = new Set(
            probeResults
              .filter(result => result.probed)
              .map(result => result.dirName)
          );
        }

        this.probedDirs = probedDirs;
        if (conflictingDirs.length > 0) {
          // Store the conflicting folder name (take the first one for now)
          this.conflictingFolder = conflictingDirs[0];
          this.pendingItems = items;

          this.onConflict(
            (resolution) => this.resolveConflict(basePath, items, resolution),
            { allowRename: true }
          );
          return;
        }
        // Store probed directories so we can skip them in queue processing
        this.probedDirs = probedDirs;
      }

      // Loose files dropped into the folder being viewed: their names can be checked
      // against the current listing (renaming is only offered for folders).
      const currentDir = state.req?.path?.endsWith("/") ? state.req.path : `${state.req?.path}/`;
      if (!getters.isShare() && currentDir === basePath) {
        const existingNames = new Set((state.req.items || []).map((i) => i.name));
        const hasLooseConflict = Array.from(items).some(
          (item) => !item.relativePath?.includes("/") && existingNames.has(item.file.name)
        );
        if (hasLooseConflict) {
          this.conflictingFolder = null;
          this.pendingItems = items;
          this.onConflict(
            (resolution) => this.resolveConflict(basePath, items, resolution),
            { allowRename: false }
          );
          return;
        }
      }
    }

    if (skipExisting) {
      const filtered = await this.filterExistingItems(basePath, items);
      if (filtered.skipped > 0) {
        notify.showSuccessToast(
          i18n.global.t("prompts.uploadSkipped", { count: filtered.skipped })
        );
      }
      if (filtered.renamed > 0) {
        notify.showSuccessToast(
          i18n.global.t("prompts.uploadRenamed", { count: filtered.renamed })
        );
      }
      items = filtered.items;
      if (items.length === 0) {
        this.pendingItems = null;
        this.conflictingFolder = null;
        this.probedDirs.clear();
        return [];
      }
    }

    const effectiveOverwrite = this.overwriteAll || overwrite;
    const dirs = new Set();
    for (const item of items) {
      if (item.relativePath) {
        const pathParts = item.relativePath.split("/");
        pathParts.pop(); // Grab the directory path by removing the filename.

        let currentPath = "";
        for (const part of pathParts) {
          currentPath += `${part}/`;
          dirs.add(currentPath);
        }
      }
    }

    const newUploads = [];

    if (dirs.size > 0) {
      // Sort paths to ensure parent directories are created before children.
      const sortedDirs = [...dirs].sort();

      for (const dir of sortedDirs) {
        const pathParts = dir.slice(0, -1).split("/");
        const dirName = pathParts[pathParts.length - 1];

        // Skip top-level directories that were already created during probing
        const probedSession = pathParts.length === 1 && this.probedDirs.has(dirName)
          ? this.probedDirectorySessions.get(dirName) : null;

        const upload = {
          id: this.nextId++,
          sessionId: probedSession || newUploadSessionId(),
          shareHash: getters.isShare() ? state.shareInfo?.hash : null,
          name: dirName,
          size: 0,
          progress: 0,
          status: probedSession ? "completed" : "pending",
          type: "directory",
          isToplevelDir: pathParts.length === 1,
          path: `${basePath}${dir}`,
          source: state.req.source,
          // Existing folders are merged into when skipping existing files.
          overwrite: effectiveOverwrite || skipExisting,
        };

        newUploads.push(upload);
      }
    }

    const fileUploads = Array.from(items).map((item) => {
      const id = this.nextId++;
      const file = item.file;
      const relativePath = item.relativePath || file.name;
      const destinationPath = `${basePath}${relativePath}`;
      const upload = {
        id,
        sessionId: newUploadSessionId(),
        shareHash: getters.isShare() ? state.shareInfo?.hash : null,
        file,
        name: item.uploadName || file.name,
        size: file.size,
        progress: 0,
        chunkOffset: 0,
        status: "pending", // pending, uploading, paused, completed, error
        xhr: null,
        path: destinationPath, // Full destination path
        source: state.req.source,
        overwrite: effectiveOverwrite || item.overwriteExisting === true,
        lastProgressTime: null, // Track when progress was last updated
        connectionIssue: false, // Flag for connection-related issues
      };
      return upload;
    });

    const dirCount = newUploads.length;
    if (fileUploads.length >= 50) {
      console.info(
        `[FileBrowser upload] add(): ${fileUploads.length} file job(s), ${dirCount} directory job(s) → base ${basePath}`
      );
    }

    this.queue.push(...newUploads, ...fileUploads);
    for (const upload of newUploads) this.cleanupDirectories.set(upload.id, upload);

    // Clean up pending items after successful add
    this.pendingItems = null;
    this.conflictingFolder = null;
    this.probedDirs.clear();
    this.probedDirectorySessions.clear();

    void this.processQueue();
    return newUploads;
  }

  async processQueue() {
    if (this.isPausedForConflict) {
      return;
    }

    if (this.isOverallPaused) {
      return;
    }

    const maxConcurrent = state.user.fileLoading?.maxConcurrentUpload || 3;
    while (
      this.activeUploads < maxConcurrent &&
      this.hasPending()
    ) {
      const upload = this.queue.find((item) => item.status === "pending" && !item.running && this.directoryParentsReady(item));
      if (upload) {
        if (this.overwriteAll) {
          upload.overwrite = true;
        }
        this.start(upload.id);
      } else break;
    }

    // Update isUploading state based on whether there are active or pending uploads
    const hasActiveOrPending = this.activeUploads > 0 || this.hasPending();
    mutations.setIsUploading(hasActiveOrPending);

    // Only reload when we transition from having active uploads to having none
    const hasNoActiveOrPending = this.activeUploads === 0 && !this.hasPending();
    if (this.hadActiveUploads && hasNoActiveOrPending) {
      console.log("all uploads processed  ", this.queue);
      // Only reload if there are no errors or conflicts - keep prompt open so users can see and retry
      const hasErrorsOrConflicts = this.queue.some((item) =>
        item.status === "error" || item.status === "conflict"
      );
      if (!hasErrorsOrConflicts) {
        mutations.setReload(true);
      }
      if (this.queue.every(upload => upload.status === 'completed')) this.cleanupDirectories.clear();
      this.hadActiveUploads = false; // Reset the flag
      this.overwriteAll = null; // Reset for next batch of uploads
    }
  }

  directoryParentsReady(upload) {
    const path = `${upload.path.replace(/\/+$/, '')}/`;
    return ![...this.cleanupDirectories.values()].some(directory => {
      if (directory.id === upload.id || directory.source !== upload.source || directory.shareHash !== upload.shareHash) return false;
      const parent = `${directory.path.replace(/\/+$/, '')}/`;
      return path !== parent && path.startsWith(parent) && directory.status !== 'completed';
    });
  }

  start(id) {
    const upload = this.findById(id);
    if (upload?.status !== "pending") {
      console.log(
        `upload.js: Cannot start upload for id ${id}. Status is not 'pending' or upload not found.`,
        upload
      );
      return;
    }

    if (upload.type === "directory") {
      void this.startDirectoryUpload(upload);
    } else {
      void this.startFileUpload(upload);
    }
  }

  async startDirectoryUpload(upload) {
    this.activeUploads++;
    upload.running = true;
    this.hadActiveUploads = true; // Mark that we've had active uploads
    upload.status = "uploading";

    try {
      const headers = { 'X-File-Upload-Session': upload.sessionId };
      const promise = upload.shareHash
        ? resourcesApi.postPublic(upload.shareHash, upload.path, new Blob([]), upload.overwrite, undefined, headers, true)
        : resourcesApi.post(upload.source, upload.path, new Blob([]), upload.overwrite, undefined, headers, true);
      upload.xhrPromise = promise;
      await promise;

      if (upload.status === "uploading") {
        upload.status = "completed";
        upload.progress = 100;
        notifyUploadComplete(upload);
      }
    } catch (err) {
      await this.handleUploadError(upload, err);
    } finally {
      this.activeUploads--;
      upload.running = false;
      upload.xhrPromise = null;
      void this.processQueue();
    }
  }

  async startFileUpload(upload) {
    this.activeUploads++;
    upload.running = true;
    this.hadActiveUploads = true; // Mark that we've had active uploads
    upload.status = "uploading";

    // Default to 5 MB when unset; explicit 0 disables chunking (single-shot upload).
    const chunkSizeMb = state.user.fileLoading?.uploadChunkSizeMb ?? 5;
    const chunkSize = chunkSizeMb * 1024 * 1024;

    // Single-shot upload when chunking is disabled or file is smaller than chunk size.
    if (chunkSize === 0 || upload.size < chunkSize) {
      const progress = (percent) => {
        if (percent === 100) upload.verifyingIntegrity = true;
        this.updateProgress(upload, percent);
      };

      try {
        const integrity = await prepareUploadIntegrity(upload, upload.file, false, () => this.recordUploadActivity(upload));
        if (upload.status !== "uploading") throw new Error("Upload aborted");
        await this.sendFileRequest(upload, upload.file, progress, {
          "X-File-Upload-Session": upload.sessionId,
          "X-File-Total-Size": upload.size,
          ...integrity.headers,
        });

        this.recordUploadActivity(upload);
        // Clear timeout on successful completion
        this.clearProgressTimeout(upload.id);
        upload.status = "completed";
        upload.progress = 100;
        upload.connectionIssue = false;
        notifyUploadComplete(upload);
      } catch (err) {
        this.clearProgressTimeout(upload.id);
        await this.handleUploadError(upload, err);
      } finally {
        upload.verifyingIntegrity = false;
        this.activeUploads--;
        upload.running = false;
        upload.xhr = null;
        upload.xhrPromise = null;
        void this.processQueue();
      }
      return;
    }

    // Chunked upload loop: The server supports resuming from any chunk offset.
    // Each chunk is written atomically at the specified offset using Seek().
    // If we pause/error during a chunk upload, that chunk will be retried on resume.
    // chunkOffset is only incremented after successful chunk completion, so resuming
    // will retry the current chunk if it was incomplete.
    while (upload.chunkOffset < upload.size && upload.status === "uploading") {
      const chunk = upload.file.slice(
        upload.chunkOffset,
        upload.chunkOffset + chunkSize
      );

      const chunkProgress = (percent) => {
        if (percent === 100 && upload.chunkOffset + chunk.size === upload.size) upload.verifyingIntegrity = true;
        const chunkLoaded = (percent / 100) * chunk.size;
        const totalLoaded = upload.chunkOffset + chunkLoaded;
        const progress = (totalLoaded / upload.size) * 100;
        this.updateProgress(upload, Math.round(progress * 10) / 10);
      };

      try {
        const integrity = await prepareUploadIntegrity(upload, chunk, true, () => this.recordUploadActivity(upload));
        if (upload.status !== "uploading") throw new Error("Upload aborted");
        const response = await this.sendFileRequest(upload, chunk, chunkProgress, {
          "X-File-Upload-Session": upload.sessionId,
          "X-File-Chunk-Offset": upload.chunkOffset,
          "X-File-Total-Size": upload.size,
          ...integrity.headers,
        });
        validateChunkAcknowledgement(response, upload.chunkOffset + chunk.size, upload.size);
        upload.verifyingIntegrity = false;

        // Only increment chunkOffset after successful chunk upload.
        // This ensures that if we pause/error mid-chunk, we'll retry that chunk on resume.
        upload.chunkOffset += chunk.size;
        // A completed chunk is activity even when no progress event was emitted.
        this.recordUploadActivity(upload);
      } catch (err) {
        this.clearProgressTimeout(upload.id);
        await this.handleUploadError(upload, err);
        break; // Exit loop on error or pause
      }
    }

    // Clear timeout when upload finishes
    this.clearProgressTimeout(upload.id);

    if (upload.status === "uploading") {
      // If the loop finished without being paused/errored
      upload.status = "completed";
      upload.progress = 100;
      notifyUploadComplete(upload);
      upload.connectionIssue = false;
    }

    this.activeUploads--;
    upload.running = false;
    upload.verifyingIntegrity = false;
    upload.xhr = null;
    upload.xhrPromise = null;
    void this.processQueue();
  }

  isConnectionError(err) {
    const status = err?.status ?? err?.response?.status;
    if (status) return false;
    return err?.code === "UPLOAD_IDLE" || /network|timeout|timed out|connection|failed to fetch/i.test(err?.message || "");
  }

  isRetryableError(err) {
    const status = err?.status ?? err?.response?.status;
    return this.isConnectionError(err) || [408, 425, 429, 500, 502, 503, 504].includes(status);
  }

  async sendFileRequest(upload, body, progress, headers) {
    upload.failedAttempts = 0;
    for (let attempt = 0; ; attempt++) {
      if (upload.status !== "uploading" || !this.findById(upload.id)) throw new Error("Upload aborted");
      upload.retrying = false;
      upload.idleError = null;
      upload.connectionIssue = false;
      this.recordUploadActivity(upload);
      this.startProgressTimeout(upload);
      try {
        const guardedProgress = percent => {
          if (!upload.idleError && upload.status === "uploading") progress(percent);
        };
        const promise = getters.isShare()
          ? resourcesApi.postPublic(state.shareInfo?.hash, upload.path, body, upload.overwrite, guardedProgress, headers, false, { quietErrors: true })
          : resourcesApi.post(upload.source, upload.path, body, upload.overwrite, guardedProgress, headers, false, { quietErrors: true });
        upload.xhrPromise = promise;
        upload.xhr = promise.xhr;
        const response = await promise;
        if (upload.status !== "uploading" || !this.findById(upload.id)) throw new Error("Upload aborted");
        return response;
      } catch (error) {
        const err = upload.idleError || error;
        if (upload.status !== "uploading" || !this.findById(upload.id)) throw new Error("Upload aborted", { cause: error });
        if (!this.isRetryableError(err) || attempt >= this.MAX_REQUEST_RETRIES) {
          upload.failedAttempts = attempt;
          throw err;
        }
        this.clearProgressTimeout(upload.id);
        upload.retrying = true;
        upload.retryAttempt = attempt + 1;
        upload.retrySeconds = Math.min(2 ** attempt, 10);
        upload.retryReason = this.formatErrorMessage(err);
        upload.connectionIssue = true;
        upload.verifyingIntegrity = false;
        upload.progress = upload.size ? Math.round(upload.chunkOffset / upload.size * 1000) / 10 : 0;
        await new Promise((resolve, reject) => {
          const timer = setTimeout(() => { upload.cancelRetry = null; resolve(); }, upload.retrySeconds * 1000);
          upload.cancelRetry = () => { clearTimeout(timer); upload.cancelRetry = null; reject(new Error("Upload aborted")); };
        });
      } finally {
        this.clearProgressTimeout(upload.id);
        upload.xhrPromise = null;
        upload.xhr = null;
      }
    }
  }

  chunkSizeBytes() {
    const chunkSizeMb = state.user.fileLoading?.uploadChunkSizeMb ?? 5;
    return chunkSizeMb * 1024 * 1024;
  }

  async pauseAll() {
    this.isOverallPaused = true;
    const jobs = this.queue
      .filter((upload) => upload.status === "uploading")
      .map((upload) => this.pause(upload.id));
    await Promise.all(jobs);
  }

  resumeAll() {
    this.isOverallPaused = false;
    this.queue.forEach((upload) => {
      if (upload.status === "paused") {
        this.resume(upload.id);
      }
    });
  }

  activeXhr(upload) {
    return upload?.xhrPromise?.xhr || upload?.xhr || null;
  }

  /** Abort the in-flight upload operation, including a pending 401 renew+retry. */
  abortUpload(upload) {
    if (upload?.xhrPromise && typeof upload.xhrPromise.abort === "function") {
      upload.xhrPromise.abort();
      return;
    }
    const xhr = this.activeXhr(upload);
    if (xhr && xhr.readyState !== XMLHttpRequest.DONE) {
      xhr.abort();
    }
  }

  async pause(id) {
    const upload = this.findById(id);
    if (upload?.status !== "uploading") {
      return;
    }
    upload.status = "paused";
    upload.retrying = false;
    upload.cancelRetry?.();
    this.clearProgressTimeout(id);
    // Start the best-effort pause signal before aborting, without waiting for
    // another browser connection to become available.
    let signal;
    if (upload.type !== "directory" && this.chunkSizeBytes() > 0 && upload.size >= this.chunkSizeBytes()) {
      signal = getters.isShare()
        ? resourcesApi.signalUploadPause(null, upload.path, state.shareInfo.hash)
        : resourcesApi.signalUploadPause(upload.source, upload.path, null);
    }
    this.abortUpload(upload);
    try { await signal; } catch (e) { console.warn("upload pause signal failed", e); }
  }

  clearProgressTimeout(id) {
    const timeoutId = this.progressTimeouts.get(id);
    if (timeoutId) {
      clearTimeout(timeoutId);
      this.progressTimeouts.delete(id);
    }
  }

  recordUploadActivity(upload) {
    upload.lastProgressTime = Date.now();
    this.lastUploadActivityTime = upload.lastProgressTime;
  }

  startProgressTimeout(upload, delay = this.PROGRESS_TIMEOUT_MS) {
    // Clear any existing timeout
    this.clearProgressTimeout(upload.id);

    // Requests can wait for bandwidth or a browser connection while siblings upload.
    const timeoutId = setTimeout(() => {
      this.progressTimeouts.delete(upload.id);
      if (upload.status === "uploading") {
        if (upload.preparingIntegrity || upload.retrying) {
          this.startProgressTimeout(upload);
          return;
        }
        const lastActivity = Math.max(
          upload.lastProgressTime ?? 0,
          this.lastUploadActivityTime ?? 0
        );
        const idleTime = Date.now() - lastActivity;
        const limit = upload.verifyingIntegrity ? this.VERIFY_TIMEOUT_MS : this.PROGRESS_TIMEOUT_MS;
        if (idleTime < limit) {
          this.startProgressTimeout(upload, limit - idleTime);
          return;
        }
        upload.idleError = Object.assign(new Error("Upload connection timed out"), { code: "UPLOAD_IDLE" });
        this.abortUpload(upload);
      }
    }, delay);

    this.progressTimeouts.set(upload.id, timeoutId);
  }

  updateProgress(upload, progress) {
    if (upload.status !== "uploading" || !this.findById(upload.id)) {
      return;
    }
    upload.progress = progress;
    this.recordUploadActivity(upload);
    upload.connectionIssue = false; // Clear connection issue on successful progress
    // Reset the timeout whenever we get progress
    this.startProgressTimeout(upload);
  }

  resume(id) {
    const upload = this.findById(id);
    if (upload?.status === "paused") {
      this.isOverallPaused = false;
      upload.status = "pending";
      upload.connectionIssue = false; // Clear connection issue on resume
      const progress =
        upload.size > 0 ? (upload.chunkOffset / upload.size) * 100 : 0;
      upload.progress = Math.round(progress * 10) / 10;
      void this.processQueue();
    }
  }

  stopForCancellation(upload) {
    upload.status = "cancelled";
    upload.cancelling = true;
    upload.cleanupPending = false;
    upload.retrying = false;
    upload.cancelRetry?.();
    this.clearProgressTimeout(upload.id);
    this.abortUpload(upload);
  }

  async cancel(id) {
    const upload = this.findById(id) || this.cleanupDirectories.get(id);
    if (!upload) return true;
    if (upload.status === 'completed') {
      const index = this.queue.findIndex(item => item.id === id);
      if (index !== -1) this.queue.splice(index, 1);
      return true;
    }
    if (upload.cleanupPromise) return upload.cleanupPromise;
    this.stopForCancellation(upload);
    upload.cleanupPromise = (async () => {
      try {
        const result = upload.sessionId ? await resourcesApi.cancelUpload(upload.source, upload.path, upload.sessionId, upload.shareHash) : null;
        if (!result?.ownedDirectory || result.directoryRemoved) this.cleanupDirectories.delete(id);
        if (!this.control.cancelling && upload.type !== 'directory' && this.queue.every(item => item.id === id || item.status === 'completed' || item.cleanupPending)) {
          const dirs = [...this.cleanupDirectories.values()].sort((a, b) => b.path.split('/').length - a.path.split('/').length);
          for (const dir of dirs) { this.stopForCancellation(dir); await this.cancel(dir.id); }
        }
        const index = this.queue.findIndex(item => item.id === id);
        if (index !== -1) this.queue.splice(index, 1);
        if (!this.control.cancelling && this.queue.every(item => item.status === 'completed')) this.cleanupDirectories.clear();
        mutations.setReload(true);
        return true;
      } catch (error) {
        upload.status = 'error';
        upload.cleanupPending = true;
        upload.errorDetails = i18n.global.t('transfer.uploadCleanupFailed', { reason: this.formatErrorMessage(error) });
        if (!this.findById(id)) this.queue.push(upload);
        return false;
      } finally {
        upload.cancelling = false;
        upload.cleanupPromise = null;
      }
    })();
    return upload.cleanupPromise;
  }

  async cancelAll() {
    if (this.control.cancelling) return;
    this.control.cancelling = true;
    this.isOverallPaused = true;
    const jobs = [...new Map([
      ...this.queue.filter(upload => upload.status !== 'completed'),
      ...this.cleanupDirectories.values(),
    ].map(upload => [upload.id, upload])).values()];
    jobs.forEach(upload => this.stopForCancellation(upload));
    try {
      const files = jobs.filter(upload => upload.type !== 'directory');
      for (let i = 0; i < files.length; i += 4) await Promise.all(files.slice(i, i + 4).map(upload => this.cancel(upload.id)));
      const dirs = jobs.filter(upload => upload.type === 'directory').sort((a, b) => b.path.split('/').length - a.path.split('/').length);
      for (const upload of dirs) await this.cancel(upload.id);
      if (!jobs.some(upload => upload.cleanupPending)) notify.showSuccessToast(i18n.global.t('transfer.uploadCancelled'));
    } finally {
      this.control.cancelling = false;
      this.isOverallPaused = false;
      void this.processQueue();
    }
  }

  retry(id, overwrite) {
    const upload = this.findById(id);
    if (upload?.cleanupPending) return this.cancel(id);
    if (upload && ["error", "conflict"].includes(upload.status)) {
      if (typeof overwrite === "boolean") upload.overwrite = overwrite;
      upload.status = "pending";
      upload.connectionIssue = false; // Clear connection issue on retry
      if (upload.error?.status === 416 || upload.error?.status === 422 || upload.restartRequired) {
        upload.chunkOffset = 0;
        upload.sessionId = newUploadSessionId();
        upload.restartRequired = false;
      }
      const isChunked =
        upload.type !== "directory" && upload.size >= this.chunkSizeBytes();
      const canResume = isChunked && upload.chunkOffset > 0;
      if (!canResume) {
        if (upload.type !== "directory") {
          upload.chunkOffset = 0;
        }
        upload.progress = 0;
      } else {
        // Resume chunked uploads from the last successful chunk.
        upload.progress =
          upload.size > 0
            ? Math.round((upload.chunkOffset / upload.size) * 1000) / 10
            : 0;
      }
      void this.processQueue();
    }
  }

  clearCompleted() {
    let hadCompleted = false;
    for (let i = this.queue.length - 1; i >= 0; i--) {
      const upload = this.queue.at(i);
      const status = upload.status;
      if (status === "completed") {
        this.clearProgressTimeout(upload.id);
        this.queue.splice(i, 1);
        hadCompleted = true;
      }
      if (state.user.fileLoading?.clearAll) {
        if (status === "error" || status === "conflict" || status === "paused") {
          void this.cancel(upload.id);
        }
      }
    }
    // If we had completed uploads and the queue is now empty, trigger reload
    if (hadCompleted && this.queue.length === 0) {
      mutations.setReload(true);
    }
  }

  findById(id) {
    return this.queue.find((item) => item.id === id);
  }

  hasPending() {
    return this.queue.some((item) => item.status === "pending");
  }

  getConflictingFolder() {
    return this.conflictingFolder;
  }

  async renameFolder(oldName, newName) {
    if (!oldName || !newName || !this.pendingItems) {
      throw new Error("Invalid parameters for folder rename");
    }

    // Update all items in the pending items that reference the old folder name
    const updatedItems = this.pendingItems.map(item => {
      if (item.relativePath) {
        const pathParts = item.relativePath.split("/");
        if (pathParts[0] === oldName) {
          // Replace the first part of the path with the new name
          pathParts[0] = newName;
          return {
            ...item,
            relativePath: pathParts.join("/")
          };
        }
      }
      return item;
    });

    // Store the updated items for processing
    this.pendingItems = updatedItems;
    return true;
  }

  async handleUploadError(upload, err) {
    if (upload.cancelling || upload.status === 'cancelled') return;
    upload.verifyingIntegrity = false;
    upload.retrying = false;
    upload.restartRequired = err?.status === 416 || err?.status === 422;
    // Check if the error is a 409 Conflict
    if ((err?.status ?? err?.response?.status) === 409) {
      upload.status = "conflict";
      upload.connectionIssue = false;
    } else if (err.message !== "Upload aborted") {
      // Detect connection-related errors
      const isConnectionError = this.isConnectionError(err);

      upload.status = "error";
      upload.connectionIssue = isConnectionError;
      upload.errorDetails = upload.failedAttempts
        ? i18n.global.t("transfer.uploadRetriesExhausted", { count: upload.failedAttempts, reason: this.formatErrorMessage(err) })
        : this.formatErrorMessage(err);
      notifyUploadError(upload.name, upload.errorDetails);
    } else {
      // Upload was aborted - preserve connectionIssue flag if it was already set
      // (e.g., if we paused due to timeout, keep the connection issue flag)
      const hadConnectionIssue = upload.connectionIssue;
      console.log(`upload.js: Upload aborted for id ${upload.id}`, upload);
      // Only clear connectionIssue if it wasn't already set (user-initiated pause)
      if (!hadConnectionIssue) {
        upload.connectionIssue = false;
      }
    }
  }

  formatErrorMessage(err) {
    if (err?.code === "UPLOAD_IDLE") return i18n.global.t("transfer.uploadIdle");
    if (err?.response) {
      // API error with response
      const status = err.response.status;
      const statusText = err.response.statusText;
      const message = err.response.data?.message || err.response.data?.error || statusText;
      return `${status} ${statusText}: ${message}`;
    } else if (err?.message) {
      // Network error or other error with message
      return err.message;
    } else {
      // Fallback for unknown errors
      return "Unknown error occurred";
    }
  }
}

export const uploadManager = new UploadManager();

// Check for conflicts between items to be uploaded/moved and existing items
export function checkConflict(newItems, existingItems) {
  if (!newItems || !existingItems) {
    return false;
  }

  const existingNames = new Set(existingItems.map(item => item.name));

  return newItems.some(item => {
    const itemName = item.name || item.file?.name;
    return itemName && existingNames.has(itemName);
  });
}
