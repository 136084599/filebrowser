import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
// Queue/timing tests isolate the asynchronous hashing work. Integrity and strict
// acknowledgements are independently tested against known SHA-256 vectors.
vi.mock("@/utils/uploadIntegrity", () => ({
  prepareUploadIntegrity: vi.fn(async () => ({ headers: {}, accept() {} })),
  validateChunkAcknowledgement: vi.fn(),
}));

vi.mock("@/api", () => ({
  resourcesApi: {
    post: vi.fn(),
    postPublic: vi.fn(),
    signalUploadPause: vi.fn(),
    cancelUpload: vi.fn(),
    listDirectoryEntries: vi.fn(),
  },
}));
vi.mock("@/notify", () => ({ notify: { showSuccessToast: vi.fn() } }));
vi.mock("@/i18n", () => ({ default: { global: { t: (key, params) => `${key}:${params?.count}` } } }));
vi.mock("@/store", () => ({
  state: {
    req: { source: "test", path: "/base/", items: [] },
    user: { fileLoading: { maxConcurrentUpload: 10, uploadChunkSizeMb: 5 } },
    shareInfo: { hash: "share" },
  },
  mutations: { setIsUploading: vi.fn(), setReload: vi.fn() },
}));
vi.mock("@/store/getters", () => ({ getters: { isShare: vi.fn(() => false) } }));
vi.mock("@/utils/appNotifications", () => ({
  notifyUploadComplete: vi.fn(),
  notifyUploadError: vi.fn(),
}));

import { resourcesApi } from "@/api";
import { notify } from "@/notify";
import { state } from "@/store";
import { getters } from "@/store/getters";
import { isSameSize, numberedName, uploadManager } from "./upload";

function addUpload(status = "uploading", file = new Blob(["a"])) {
  uploadManager.queue.push({
    id: uploadManager.nextId++,
    type: "file",
    source: "test",
    path: "/file",
    file,
    size: file.size,
    status,
    progress: 0,
    chunkOffset: 0,
    lastProgressTime: Date.now(),
    connectionIssue: false,
  });
  return uploadManager.queue.at(-1);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
  vi.clearAllMocks();
  resourcesApi.post.mockReset();
  resourcesApi.postPublic.mockReset();
  vi.spyOn(console, "log").mockImplementation(() => {});
  getters.isShare.mockReturnValue(false);
  uploadManager.queue.splice(0);
  uploadManager.activeUploads = 0;
  uploadManager.nextId = 0;
  uploadManager.isOverallPaused = false;
  uploadManager.hadActiveUploads = false;
  uploadManager.lastUploadActivityTime = null;
  uploadManager.cleanupDirectories.clear();
  uploadManager.probedDirectorySessions.clear();
  uploadManager.control.cancelling = false;
  resourcesApi.cancelUpload.mockReset();
  resourcesApi.cancelUpload.mockResolvedValue({ cancelled: true });
});

afterEach(() => {
  for (const id of uploadManager.progressTimeouts.keys()) {
    uploadManager.clearProgressTimeout(id);
  }
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("upload connection recovery", () => {
  it("does not pause or abort after a normal 16-second wait", async () => {
    const upload = addUpload();
    upload.xhrPromise = { abort: vi.fn() };
    uploadManager.startProgressTimeout(upload);
    await vi.advanceTimersByTimeAsync(16000);
    expect(upload.status).toBe("uploading");
    expect(upload.xhrPromise.abort).not.toHaveBeenCalled();
  });

  it("lets queued requests wait while sibling requests make progress", async () => {
    const upload = addUpload();
    upload.xhrPromise = { abort: vi.fn() };
    const sibling = addUpload();
    uploadManager.startProgressTimeout(upload);
    for (let i = 0; i < 30; i++) {
      await vi.advanceTimersByTimeAsync(5000);
      uploadManager.updateProgress(sibling, 1);
    }
    expect(upload.xhrPromise.abort).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(120000);
    expect(upload.xhrPromise.abort).toHaveBeenCalledTimes(1);
    expect(upload.idleError.code).toBe("UPLOAD_IDLE");
    expect(upload.status).toBe("uploading");
  });

  it("allows slow final verification without a short transfer timeout", async () => {
    const upload = addUpload();
    upload.verifyingIntegrity = true;
    upload.xhrPromise = { abort: vi.fn() };
    uploadManager.startProgressTimeout(upload);
    await vi.advanceTimersByTimeAsync(120000);
    expect(upload.xhrPromise.abort).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(uploadManager.VERIFY_TIMEOUT_MS - 120000);
    expect(upload.xhrPromise.abort).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])("automatically retries the same unconfirmed chunk (share=%s)", async share => {
    getters.isShare.mockReturnValue(share);
    const post = share ? resourcesApi.postPublic : resourcesApi.post;
    post.mockResolvedValueOnce("").mockRejectedValueOnce(new Error("Network error")).mockResolvedValueOnce("");
    const upload = addUpload("pending", new Blob([new Uint8Array(5 * 1024 * 1024 + 1)]));
    const done = uploadManager.startFileUpload(upload);
    await vi.advanceTimersByTimeAsync(0);
    expect(upload.chunkOffset).toBe(5 * 1024 * 1024);
    expect(upload.retrying).toBe(true);
    expect(upload.status).toBe("uploading");
    await vi.advanceTimersByTimeAsync(1000);
    await done;
    expect(upload.status).toBe("completed");
    expect(upload.chunkOffset).toBe(upload.size);
    expect(post.mock.calls.map(call => call[5]["X-File-Chunk-Offset"])).toEqual([0, 5 * 1024 * 1024, 5 * 1024 * 1024]);
    expect(uploadManager.progressTimeouts.size).toBe(0);
  });

  it.each([403, 409, 413, 422])("does not retry a permanent HTTP %s error or disguise it as a pause", async status => {
    resourcesApi.post.mockRejectedValueOnce(Object.assign(new Error("HTTP failure"), { status }));
    const upload = addUpload("pending");
    await uploadManager.startFileUpload(upload);
    expect(resourcesApi.post).toHaveBeenCalledTimes(1);
    expect(upload.status).toBe(status === 409 ? "conflict" : "error");
    expect(upload.connectionIssue).toBe(false);
  });

  it("stops with a visible error after five automatic retries", async () => {
    resourcesApi.post.mockRejectedValue(new Error("Network error"));
    const upload = addUpload("pending");
    const done = uploadManager.startFileUpload(upload);
    await vi.runAllTimersAsync();
    await done;
    expect(resourcesApi.post).toHaveBeenCalledTimes(6);
    expect(upload.status).toBe("error");
    expect(upload.errorDetails).toContain("transfer.uploadRetriesExhausted");
    expect(uploadManager.activeUploads).toBe(0);
    expect(uploadManager.progressTimeouts.size).toBe(0);
  });

  it.each(["pause", "cancel"])("cancels the retry timer immediately on manual %s", async action => {
    resourcesApi.post.mockRejectedValue(new Error("Network error"));
    const upload = addUpload("pending");
    const done = uploadManager.startFileUpload(upload);
    await vi.advanceTimersByTimeAsync(0);
    expect(upload.retrying).toBe(true);
    if (action === "pause") await uploadManager.pause(upload.id);
    else uploadManager.cancel(upload.id);
    await done;
    await vi.advanceTimersByTimeAsync(30000);
    expect(resourcesApi.post).toHaveBeenCalledTimes(1);
    expect(upload.status).toBe(action === "pause" ? "paused" : "cancelled");
    expect(uploadManager.activeUploads).toBe(0);
  });

  it("aborts a stalled request and retries automatically without manual resume", async () => {
    let rejectRequest;
    const stalled = new Promise((_, reject) => { rejectRequest = reject; });
    stalled.abort = () => rejectRequest(new Error("Upload aborted"));
    resourcesApi.post.mockReturnValueOnce(stalled).mockResolvedValueOnce("");
    const upload = addUpload("pending");
    const done = uploadManager.startFileUpload(upload);
    await vi.advanceTimersByTimeAsync(120000);
    expect(upload.retrying).toBe(true);
    expect(upload.status).toBe("uploading");
    await vi.advanceTimersByTimeAsync(1000);
    await done;
    expect(resourcesApi.post).toHaveBeenCalledTimes(2);
    expect(upload.status).toBe("completed");
  });

  it("waits for the previous run to settle when a user immediately pauses and resumes", async () => {
    resourcesApi.post.mockRejectedValueOnce(new Error("Network error")).mockResolvedValueOnce("");
    const upload = addUpload("pending");
    const done = uploadManager.startFileUpload(upload);
    await vi.advanceTimersByTimeAsync(0);
    void uploadManager.pause(upload.id);
    uploadManager.resume(upload.id);
    expect(resourcesApi.post).toHaveBeenCalledTimes(1);
    await done;
    await vi.advanceTimersByTimeAsync(0);
    expect(resourcesApi.post).toHaveBeenCalledTimes(2);
    expect(upload.status).toBe("completed");
    expect(uploadManager.activeUploads).toBe(0);
  });
});


describe("numberedName", () => {
  it("puts the suffix before the extension", () => {
    expect(numberedName("DSCF8750.MOV", 1)).toBe("DSCF8750_01.MOV");
    expect(numberedName("a.b.mov", 12)).toBe("a.b_12.mov");
    expect(numberedName("README", 2)).toBe("README_02");
    expect(numberedName(".env", 1)).toBe(".env_01");
  });
});

describe("isSameSize", () => {
  it("accepts the exact size and the 4 KiB block-rounded disk usage", () => {
    expect(isSameSize(2500000, 2500000)).toBe(true);
    expect(isSameSize(2502656, 2500000)).toBe(true);
    expect(isSameSize(0, 0)).toBe(true);
  });

  it("rejects empty or otherwise different remote files", () => {
    expect(isSameSize(0, 2500000)).toBe(false);
    expect(isSameSize(4096, 2500000)).toBe(false);
    expect(isSameSize(2506752, 2500000)).toBe(false);
    expect(isSameSize(4096, 0)).toBe(false);
  });
});

describe("skip existing files", () => {
  const fileOf = (name, size) => ({ name, size });
  const item = (relativePath, size) => ({
    file: fileOf(relativePath.split("/").pop(), size),
    relativePath,
  });

  it("skips same-size files, keeps missing ones and flags size mismatches for replacement", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([
        ["done.mov", { size: 100, type: "file" }],
        ["empty.mov", { size: 0, type: "file" }],
      ])
    );
    const { items, skipped } = await uploadManager.filterExistingItems("/base/", [
      item("Cam/done.mov", 100),
      item("Cam/empty.mov", 500),
      item("Cam/new.mov", 300),
    ]);

    expect(resourcesApi.listDirectoryEntries).toHaveBeenCalledWith("test", "/base/Cam/");
    expect(skipped).toBe(1);
    expect(items.map((i) => i.relativePath)).toEqual(["Cam/empty.mov", "Cam/new.mov"]);
    expect(items[0].overwriteExisting).toBe(true);
    expect(items[1].overwriteExisting).toBeUndefined();
  });

  it("treats every file as missing when the destination folder can't be listed", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(null);
    const { items, skipped } = await uploadManager.filterExistingItems("/base/", [
      item("Cam/sub/a.mov", 1),
    ]);
    expect(skipped).toBe(0);
    expect(items).toHaveLength(1);
    expect(items[0].overwriteExisting).toBeUndefined();
  });

  it("uploads under a numbered name when the existing entry is a folder", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([["a.mov", { size: 1, type: "directory" }]])
    );
    const { items, skipped } = await uploadManager.filterExistingItems("/base/", [item("a.mov", 1)]);
    expect(skipped).toBe(0);
    expect(items[0].relativePath).toBe("a_01.mov");
    expect(items[0].overwriteExisting).toBeUndefined();
  });

  it("never overwrites a non-empty file of a different size: uploads it as name_01, then name_02", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([
        ["clip.mov", { size: 100, type: "file" }],
        ["clip_01.mov", { size: 100, type: "file" }],
      ])
    );
    const { items, skipped, renamed } = await uploadManager.filterExistingItems("/base/", [
      item("Cam/clip.mov", 500),
    ]);
    // "Cam/" listing is the mocked one above, so the numbered names are taken from it
    expect(skipped).toBe(0);
    expect(renamed).toBe(1);
    expect(items[0].relativePath).toBe("Cam/clip_02.mov");
    expect(items[0].uploadName).toBe("clip_02.mov");
    expect(items[0].overwriteExisting).toBeUndefined();
  });

  it("skips a file when a numbered copy of it with the same size already exists", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([
        ["clip.mov", { size: 100, type: "file" }],
        ["clip_01.mov", { size: 500, type: "file" }],
      ])
    );
    const { items, skipped, renamed } = await uploadManager.filterExistingItems("/base/", [
      item("Cam/clip.mov", 500),
    ]);
    expect(skipped).toBe(1);
    expect(renamed).toBe(0);
    expect(items).toHaveLength(0);
  });

  it("gives two different items of a batch different numbered names", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([["clip.mov", { size: 100, type: "file" }]])
    );
    const { items } = await uploadManager.filterExistingItems("/base/", [
      item("Cam/clip.mov", 500),
      item("Cam/clip.mov", 600),
    ]);
    expect(items.map((i) => i.relativePath)).toEqual(["Cam/clip_01.mov", "Cam/clip_02.mov"]);
  });

  it("add() with skipExisting queues only the missing/incomplete files with the right overwrite flags", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([
        ["done.mov", { size: 100, type: "file" }],
        ["empty.mov", { size: 0, type: "file" }],
      ])
    );
    vi.spyOn(uploadManager, "processQueue").mockResolvedValue();

    await uploadManager.add(
      "/base/",
      [item("Cam/done.mov", 100), item("Cam/empty.mov", 500), item("Cam/new.mov", 300)],
      false,
      true
    );

    const files = uploadManager.queue.filter((u) => u.type !== "directory");
    expect(files.map((u) => [u.name, u.overwrite])).toEqual([
      ["empty.mov", true],
      ["new.mov", false],
    ]);
    const dirs = uploadManager.queue.filter((u) => u.type === "directory");
    expect(dirs.map((u) => [u.path, u.overwrite])).toEqual([["/base/Cam/", true]]);
    expect(notify.showSuccessToast).toHaveBeenCalledWith("prompts.uploadSkipped:1");
  });

  it("add() queues a different-size file under a numbered destination path and name", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([["big.mov", { size: 4096, type: "file" }]])
    );
    vi.spyOn(uploadManager, "processQueue").mockResolvedValue();

    await uploadManager.add("/base/", [item("Cam/big.mov", 900000)], false, true);

    const [upload] = uploadManager.queue.filter((u) => u.type !== "directory");
    expect(upload.path).toBe("/base/Cam/big_01.mov");
    expect(upload.name).toBe("big_01.mov");
    expect(upload.overwrite).toBe(false);
    expect(notify.showSuccessToast).toHaveBeenCalledWith("prompts.uploadRenamed:1");
  });

  it("add() with skipExisting queues nothing when everything is already uploaded", async () => {
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([["done.mov", { size: 100, type: "file" }]])
    );
    await uploadManager.add("/base/", [item("Cam/done.mov", 100)], false, true);
    expect(uploadManager.queue).toHaveLength(0);
    expect(notify.showSuccessToast).toHaveBeenCalledWith("prompts.uploadSkipped:1");
  });
});

describe("conflicts on loose files", () => {
  const looseItem = (name, size) => ({ file: { name, size }, relativePath: name });

  afterEach(() => {
    state.req.items = [];
    uploadManager.setOnConflict(() => {});
    uploadManager.overwriteAll = null;
  });

  it("asks what to do when a loose file already exists in the current folder, without offering rename", async () => {
    state.req.items = [{ name: "done.mov", type: "video/quicktime", size: 100 }];
    const onConflict = vi.fn();
    uploadManager.setOnConflict(onConflict);

    await uploadManager.add("/base/", [looseItem("done.mov", 100), looseItem("new.mov", 5)]);

    expect(onConflict).toHaveBeenCalledTimes(1);
    expect(onConflict.mock.calls[0][1]).toEqual({ allowRename: false });
    expect(uploadManager.queue).toHaveLength(0);
  });

  it("uploads only the missing files after choosing to skip existing ones", async () => {
    state.req.items = [{ name: "done.mov", type: "video/quicktime", size: 4096 }];
    resourcesApi.listDirectoryEntries.mockResolvedValue(
      new Map([["done.mov", { size: 4096, type: "video/quicktime" }]])
    );
    vi.spyOn(uploadManager, "processQueue").mockResolvedValue();
    uploadManager.setOnConflict((resolve) => resolve({ skip: true }));

    await uploadManager.add("/base/", [looseItem("done.mov", 100), looseItem("new.mov", 5)]);
    await vi.waitFor(() => expect(uploadManager.queue).toHaveLength(1));

    expect(uploadManager.queue[0].name).toBe("new.mov");
    expect(uploadManager.queue[0].overwrite).toBe(false);
    expect(notify.showSuccessToast).toHaveBeenCalledWith("prompts.uploadSkipped:1");
  });

  it("does not ask when no loose file exists or the destination is not the current folder", async () => {
    vi.spyOn(uploadManager, "processQueue").mockResolvedValue();
    const onConflict = vi.fn();
    uploadManager.setOnConflict(onConflict);

    state.req.items = [{ name: "other.mov", type: "video/quicktime", size: 1 }];
    await uploadManager.add("/base/", [looseItem("new.mov", 5)]);
    state.req.items = [{ name: "new.mov", type: "video/quicktime", size: 1 }];
    await uploadManager.add("/elsewhere/", [looseItem("new.mov", 5)]);

    expect(onConflict).not.toHaveBeenCalled();
    expect(uploadManager.queue).toHaveLength(2);
  });
});


describe("upload cancellation cleanup", () => {
  it("stops the entire snapshot before cleanup and preserves completed files", async () => {
    const completed = addUpload("completed");
    const paused = addUpload("paused"); paused.sessionId = "paused";
    const pending = addUpload("pending"); pending.sessionId = "pending";
    const running = addUpload("uploading"); running.sessionId = "running";
    running.xhrPromise = { abort: vi.fn() };
    const directory = addUpload("completed"); directory.type = "directory";
    directory.path = "/new/child/"; directory.sessionId = "directory";
    uploadManager.cleanupDirectories.set(directory.id, directory);
    const statuses = [];
    resourcesApi.cancelUpload.mockImplementation(async () => {
      statuses.push([paused.status, pending.status, running.status]);
      return { cancelled: true };
    });
    await uploadManager.cancelAll();
    expect(statuses.every(row => row.every(status => status === "cancelled"))).toBe(true);
    expect(running.xhrPromise.abort).toHaveBeenCalled();
    expect(resourcesApi.cancelUpload).toHaveBeenCalledTimes(4);
    expect(resourcesApi.cancelUpload.mock.calls.at(-1)[2]).toBe("directory");
    expect(uploadManager.queue.map(upload => upload.id)).toEqual([completed.id]);
    expect(resourcesApi.post).not.toHaveBeenCalled();
    expect(uploadManager.control.cancelling).toBe(false);
    expect(uploadManager.isOverallPaused).toBe(false);
  });

  it("keeps a failed cleanup visible and Retry retries cleanup without uploading", async () => {
    const upload = addUpload("paused"); upload.sessionId = "cleanup-failure";
    resourcesApi.cancelUpload.mockRejectedValueOnce(new Error("connection offline"));
    await uploadManager.cancelAll();
    expect(upload.status).toBe("error");
    expect(upload.cleanupPending).toBe(true);
    expect(upload.errorDetails).toContain("uploadCleanupFailed");
    expect(uploadManager.queue).toHaveLength(1);
    await uploadManager.retry(upload.id);
    expect(resourcesApi.cancelUpload).toHaveBeenCalledTimes(2);
    expect(resourcesApi.post).not.toHaveBeenCalled();
    expect(uploadManager.queue).toHaveLength(0);
  });

  it("still cleans new empty directories after their completed rows were cleared", async () => {
    const directory = addUpload("completed"); directory.type = "directory";
    directory.sessionId = "owned-empty"; directory.path = "/new/";
    const paused = addUpload("paused"); paused.sessionId = "remaining";
    uploadManager.cleanupDirectories.set(directory.id, directory);
    uploadManager.clearCompleted();
    expect(uploadManager.queue).toHaveLength(1);
    await uploadManager.cancelAll();
    expect(resourcesApi.cancelUpload.mock.calls.map(call => call[2])).toEqual(["remaining", "owned-empty"]);
  });

  it("retries retained directory cleanup after a failed file cleanup succeeds", async () => {
    const upload = addUpload("paused"); upload.sessionId = "partial";
    const directory = addUpload("completed"); directory.type = "directory";
    directory.sessionId = "folder"; directory.path = "/new/";
    uploadManager.cleanupDirectories.set(directory.id, directory);
    resourcesApi.cancelUpload.mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ ownedDirectory: true, directoryRemoved: false })
      .mockResolvedValueOnce({ cancelled: true })
      .mockResolvedValueOnce({ ownedDirectory: true, directoryRemoved: true });
    await uploadManager.cancelAll();
    expect(uploadManager.cleanupDirectories.size).toBe(1);
    expect(upload.cleanupPending).toBe(true);
    await uploadManager.retry(upload.id);
    expect(resourcesApi.cancelUpload.mock.calls.map(call => call[2])).toEqual(["partial", "folder", "partial", "folder"]);
    expect(uploadManager.cleanupDirectories.size).toBe(0);
    expect(uploadManager.queue).toHaveLength(0);
  });

  it("removing a completed file row never requests server deletion", async () => {
    const upload = addUpload("completed"); upload.sessionId = "done";
    await uploadManager.cancel(upload.id);
    expect(resourcesApi.cancelUpload).not.toHaveBeenCalled();
    expect(uploadManager.queue).toHaveLength(0);
  });

  it("shares one cleanup request across overlapping cancel clicks", async () => {
    const upload = addUpload("paused"); upload.sessionId = "double-click";
    let finish;
    resourcesApi.cancelUpload.mockReturnValue(new Promise(resolve => { finish = resolve; }));
    const first = uploadManager.cancel(upload.id);
    const second = uploadManager.cancel(upload.id);
    expect(resourcesApi.cancelUpload).toHaveBeenCalledTimes(1);
    finish({ cancelled: true });
    await Promise.all([first, second]);
    expect(uploadManager.queue).toHaveLength(0);
  });
});
