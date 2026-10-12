import type { Sha256Digest } from "./types.ts";

const BASE64URL_ALPHABET =
  "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

function base64Url(bytes: Uint8Array): string {
  let encoded = "";
  for (let offset = 0; offset < bytes.length; offset += 3) {
    const first = bytes[offset] ?? 0;
    const second = bytes[offset + 1] ?? 0;
    const third = bytes[offset + 2] ?? 0;
    const combined = (first << 16) | (second << 8) | third;

    encoded += BASE64URL_ALPHABET[(combined >>> 18) & 0x3f];
    encoded += BASE64URL_ALPHABET[(combined >>> 12) & 0x3f];
    if (offset + 1 < bytes.length) {
      encoded += BASE64URL_ALPHABET[(combined >>> 6) & 0x3f];
    }
    if (offset + 2 < bytes.length) {
      encoded += BASE64URL_ALPHABET[combined & 0x3f];
    }
  }
  return encoded;
}

export async function sha256Digest(bytes: Uint8Array): Promise<Sha256Digest> {
  const input = new Uint8Array(bytes);
  const hash = await crypto.subtle.digest("SHA-256", input);
  return `sha256-${base64Url(new Uint8Array(hash))}`;
}
