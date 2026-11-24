import { Blob } from "node:buffer";
import { createHash } from "node:crypto";
import { describe, expect, it, vi } from "vitest";
import { prepareUploadIntegrity, validateChunkAcknowledgement, hashBlob } from "./uploadIntegrity";
import { bytesToHex } from "@noble/hashes/utils";

describe("upload integrity over plain HTTP", () => {
  it("hashes large blobs without reading the entire file at once", async () => {
    const bytes = Buffer.alloc(17 * 1024 * 1024 + 13, 0x53);
    const blob = new Blob([bytes]);
    const expected = createHash("sha256").update(bytes).digest("hex");
    expect(bytesToHex((await hashBlob(blob)).digest())).toBe(expected);
  });
  it("does no checksum work for intermediate chunks and hashes the file once at the end", async () => {
    const file = new Blob(["abc", "def"]);
    const upload = { file, size: file.size, chunkOffset: 0 };
    const read = vi.spyOn(file, "slice");
    const first = await prepareUploadIntegrity(upload, file.slice(0, 3), true);
    read.mockClear();
    const retry = await prepareUploadIntegrity(upload, file.slice(0, 3), true);
    expect(first.headers).toEqual({});
    expect(retry.headers).toEqual({});
    expect(read).toHaveBeenCalledTimes(1); // only the caller's slice, no integrity read
    expect(upload.preparingIntegrity).toBeUndefined();
    upload.chunkOffset = 3;
    const final = await prepareUploadIntegrity(upload, file.slice(3), true);
    expect(final.headers).toEqual({ "X-File-SHA256": createHash("sha256").update("abcdef").digest("hex") });
    read.mockClear();
    const finalRetry = await prepareUploadIntegrity(upload, file.slice(3), true);
    expect(finalRetry.headers).toEqual(final.headers);
    expect(read).toHaveBeenCalledTimes(1); // cached final digest, no whole-file reread
  });
  it("checks small and empty files using only the whole-file digest", async () => {
    for (const content of ["", "small file"]) {
      const file = new Blob([content]);
      const result = await prepareUploadIntegrity({ file, size: file.size }, file, false);
      expect(result.headers).toEqual({ "X-File-SHA256": createHash("sha256").update(content).digest("hex") });
    }
  });
  it("stops final checksum preparation when paused and does not cache an incomplete result", async () => {
    const file = new Blob([new Uint8Array(17 * 1024 * 1024)]);
    const upload = { file, size: file.size, chunkOffset: 16 * 1024 * 1024, status: "uploading" };
    let activity = 0;
    await expect(prepareUploadIntegrity(upload, file.slice(upload.chunkOffset), true, () => {
      if (++activity === 2) upload.status = "paused";
    })).rejects.toThrow("Upload aborted");
    expect(upload.integrityDigest).toBeUndefined();
    expect(upload.preparingIntegrity).toBe(false);
  });
  it("recomputes the checksum if the file object changes", async () => {
    const upload = { file: new Blob(["old"]), size: 3 };
    await prepareUploadIntegrity(upload, upload.file, false);
    upload.file = new Blob(["new"]);
    const result = await prepareUploadIntegrity(upload, upload.file, false);
    expect(result.headers["X-File-SHA256"]).toBe(createHash("sha256").update("new").digest("hex"));
  });
  it("rejects empty, incomplete and incorrect chunk acknowledgements", () => {
    expect(() => validateChunkAcknowledgement("", 3, 6)).toThrow();
    expect(() => validateChunkAcknowledgement({ offset: 2, total: 6, complete: false }, 3, 6)).toThrow();
    expect(() => validateChunkAcknowledgement({ offset: 6, total: 6, complete: false }, 6, 6)).toThrow();
    expect(() => validateChunkAcknowledgement({ offset: 6, total: 6, complete: true }, 6, 6)).not.toThrow();
  });
});
