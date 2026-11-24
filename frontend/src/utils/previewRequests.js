import { state } from "@/store";
import { renew } from "@/utils/auth";

const waiting = [];
let active = 0;
const cancelled = () => new DOMException("Preview cancelled", "AbortError");

function acquire(signal) {
  if (signal?.aborted) return Promise.reject(cancelled());
  return new Promise((resolve, reject) => {
    const abort = () => {
      const index = waiting.indexOf(start);
      if (index !== -1) waiting.splice(index, 1);
      reject(cancelled());
    };
    const start = () => {
      signal?.removeEventListener("abort", abort);
      active++;
      resolve(() => { active--; waiting.shift()?.(); });
    };
    if (active < 4) start();
    else { waiting.push(start); signal?.addEventListener("abort", abort, { once: true }); }
  });
}

function retryDelay(ms, signal) {
  if (signal?.aborted) return Promise.reject(cancelled());
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(cancelled()); };
    const timer = setTimeout(() => { signal?.removeEventListener("abort", abort); resolve(); }, ms);
    signal?.addEventListener("abort", abort, { once: true });
  });
}

/**
 * Fetch a preview image and return an object URL for display.
 * Pass an AbortSignal so the caller can cancel (e.g. on navigation or unmount).
 * @param {string} url - Preview API URL from getPreviewURL / getPreviewURLPublic
 * @param {AbortSignal} signal
 * @returns {Promise<string>} Blob object URL — caller must revoke when done
 */
export async function fetchPreviewImage(url, signal) {
  let renewed = false;
  for (let attempt = 0; ; attempt++) {
    try {
      const release = await acquire(signal);
      let res;
      try {
        res = await fetch(url, {
          credentials: "same-origin", signal, cache: attempt ? "reload" : "default",
          headers: { sessionId: state.sessionId },
        });
        if (!res.ok) {
          await res.body?.cancel();
          throw Object.assign(new Error(`Preview request failed: ${res.status}`), { status: res.status });
        }
        const blob = await res.blob();
        if (!blob.size) throw Object.assign(new Error("Empty preview response"), { status: 503 });
        if (signal?.aborted) throw cancelled();
        return URL.createObjectURL(blob);
      } finally { release(); }
    } catch (err) {
      if (signal?.aborted || err.name === "AbortError") throw err;
      if (err.status === 401 && !renewed && !url.includes("/public/api/")) {
        renewed = true;
        await renew();
        attempt--;
        continue;
      }
      const transient = !err.status || [408, 425, 429, 500, 502, 503, 504].includes(err.status);
      if (!transient || attempt >= 3) throw err;
      await retryDelay(1000 * 2 ** attempt, signal);
    }
  }
}
