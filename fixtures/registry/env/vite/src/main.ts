import { loadBrowserBootstrap } from "@astilba/env/browser";

import { configuration } from "../.astilba/env/browser/browser.build.ts";
import { projection } from "../.astilba/env/browser/browser.deployment.ts";

const appName = document.querySelector("#app-name");
const label = document.querySelector("#runtime-label");
if (!(appName instanceof HTMLElement) || !(label instanceof HTMLElement)) {
  throw new Error("The application shell is incomplete.");
}
appName.textContent = configuration.appName;
void loadBrowserBootstrap({
  endpoint: "/env.json",
  expectedAudience: { origin: window.location.origin },
  fetch: window.fetch.bind(window),
  projection,
  requestBaseUrl: window.location.href,
}).then(
  ({ values }) => {
    label.textContent = values.label;
  },
  () => {
    label.textContent = "Runtime configuration is unavailable.";
  }
);
