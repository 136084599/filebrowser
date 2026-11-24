import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("@/store", () => ({ state: { sessionId: "preview-session" } }));
vi.mock("@/utils/auth", () => ({ renew: vi.fn(async () => {}) }));
import { renew } from "@/utils/auth";
import { fetchPreviewImage } from "./previewRequests";

const reply = (status = 200, content = "image data") => ({ ok: status === 200, status, body: { cancel: vi.fn() }, blob: async () => new Blob([content], { type: "image/jpeg" }) });
beforeEach(() => { vi.useFakeTimers(); vi.clearAllMocks(); vi.stubGlobal("fetch", vi.fn()); vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:preview"); });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("preview recovery", () => {
  it("retries a temporary failure and bypasses the failed HTTP cache", async () => {
    fetch.mockResolvedValueOnce(reply(503)).mockResolvedValueOnce(reply());
    const pending = fetchPreviewImage("/api/resources/preview", new AbortController().signal);
    await vi.runAllTimersAsync();
    expect(await pending).toBe("blob:preview");
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[1][1].cache).toBe("reload");
  });
  it("renews a protected session once without bypassing permissions", async () => {
    fetch.mockResolvedValueOnce(reply(401)).mockResolvedValueOnce(reply());
    expect(await fetchPreviewImage("/api/resources/preview")).toBe("blob:preview");
    expect(renew).toHaveBeenCalledTimes(1);
  });
  it.each([403, 404])("does not retry permanent HTTP %s errors", async status => {
    fetch.mockResolvedValue(reply(status));
    await expect(fetchPreviewImage("/api/resources/preview")).rejects.toMatchObject({ status });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("caps active requests and removes cancelled queued requests", async () => {
    const responses = [];
    fetch.mockImplementation(() => new Promise(resolve => responses.push(resolve)));
    const controllers = Array.from({ length: 6 }, () => new AbortController());
    const pending = controllers.map(c => fetchPreviewImage("/api/resources/preview", c.signal));
    const cancelled = expect(pending[5]).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(0);
    expect(fetch).toHaveBeenCalledTimes(4);
    controllers[5].abort();
    await cancelled;
    for (const respond of responses.slice()) respond(reply());
    await vi.advanceTimersByTimeAsync(0);
    expect(fetch).toHaveBeenCalledTimes(5);
    responses[4](reply());
    await Promise.all(pending.slice(0, 5));
  });
  it("cancels retry backoff immediately when leaving the folder", async () => {
    fetch.mockResolvedValue(reply(503));
    const controller = new AbortController();
    const pending = fetchPreviewImage("/api/resources/preview", controller.signal);
    const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(0);
    controller.abort();
    await rejected;
    await vi.runAllTimersAsync();
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
