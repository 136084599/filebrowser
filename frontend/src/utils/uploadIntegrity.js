import { sha256 } from "@noble/hashes/sha256";
import { bytesToHex } from "@noble/hashes/utils";

export async function blobBytes(blob) {
  if (typeof blob.arrayBuffer === "function") return new Uint8Array(await blob.arrayBuffer());
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsArrayBuffer(blob);
  });
}

export async function hashBlob(blob, onActivity = () => {}) {
  const hash = sha256.create();
  for (let offset = 0; offset < blob.size; offset += 8 * 1024 * 1024) {
    hash.update(await blobBytes(blob.slice(offset, offset + 8 * 1024 * 1024)));
    onActivity();
  }
  return hash;
}

/** Send bytes immediately; only the final request carries a whole-file checksum.
 * Works over plain HTTP, keeps memory bounded, and reuses the digest on retries.
 */
export async function prepareUploadIntegrity(upload, chunk, chunked, onActivity) {
  const activity = () => { if (upload.status && upload.status !== "uploading") throw new Error("Upload aborted"); onActivity?.(); };
  if (chunked && upload.chunkOffset + chunk.size < upload.size) return { headers: {} };
  if (upload.integrityFile === upload.file && upload.integrityDigest) {
    return { headers: { "X-File-SHA256": upload.integrityDigest } };
  }
  upload.preparingIntegrity = true;
  try {
    activity();
    const digest = bytesToHex((await hashBlob(upload.file, activity)).digest());
    activity();
    upload.integrityFile = upload.file;
    upload.integrityDigest = digest;
    return { headers: { "X-File-SHA256": digest } };
  } finally { upload.preparingIntegrity = false; }
}

export function validateChunkAcknowledgement(raw, offset, total) {
  const response = typeof raw === "string" ? JSON.parse(raw) : raw;
  if (!response || response.offset !== offset || response.total !== total || response.complete !== (offset === total)) {
    throw new Error("Server did not confirm the expected upload chunk; retry this file.");
  }
}
