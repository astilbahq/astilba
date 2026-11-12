import { resolve } from "node:path";
import { dashboardPorts, portBlock } from "../../../../scripts/test-ports.mjs";

// This run's fixture servers. Playwright's main process reserved the block.
export const ports = dashboardPorts();
export const api = `http://127.0.0.1:${ports.api}`;
export const dev = `http://127.0.0.1:${ports.dev}`;
export const gitProvider = `http://127.0.0.1:${ports.gitProvider}`;
export const hosted = `https://127.0.0.1:${ports.hosted}`;

// The fixture describes its hosted installation and test database here.
export const fixtureInfo = resolve(
  import.meta.dirname,
  `../../../../.artifacts/p8/browser/${portBlock()}/hosted-fixture.json`,
);
