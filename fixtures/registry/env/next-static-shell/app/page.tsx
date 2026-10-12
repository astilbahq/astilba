import { configuration } from "../.astilba/env/browser/browser.build.ts";
import { BootstrapLabel } from "./bootstrap-label";

export default function Home() {
  return (
    <main>
      <h1>{configuration.appName}</h1>
      <BootstrapLabel />
    </main>
  );
}
