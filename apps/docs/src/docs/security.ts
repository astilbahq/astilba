import { withDocsBase } from "./urls.ts";

export const CONTENT_SECURITY_POLICY_ASSET_PATH = withDocsBase(
  "/_security/content-security-policy.txt"
);
export const CONTENT_SECURITY_POLICY_HEADER = "Content-Security-Policy";

const permissionsPolicy = [
  "accelerometer=()",
  "ambient-light-sensor=()",
  "autoplay=()",
  "bluetooth=()",
  "camera=()",
  "clipboard-read=(self)",
  "clipboard-write=(self)",
  "display-capture=()",
  "encrypted-media=()",
  "fullscreen=()",
  "gamepad=()",
  "geolocation=()",
  "gyroscope=()",
  "hid=()",
  "magnetometer=()",
  "microphone=()",
  "midi=()",
  "payment=()",
  "picture-in-picture=()",
  "publickey-credentials-create=()",
  "publickey-credentials-get=()",
  "screen-wake-lock=()",
  "serial=()",
  "tools=(self)",
  "usb=()",
  "web-share=()",
  "xr-spatial-tracking=()",
].join(", ");

export const GLOBAL_SECURITY_HEADERS = {
  "Permissions-Policy": permissionsPolicy,
  "Referrer-Policy": "strict-origin-when-cross-origin",
  "Strict-Transport-Security": "max-age=31536000",
} as const;
