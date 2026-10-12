import "@fontsource-variable/geist";
import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import "@astilba/tokens/css";
import "@astilba/ui/styles.css";
import "./showcase.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app";

const root = document.querySelector("#root");

if (!root) {
  throw new Error("Showcase root element not found");
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>
);
