"use client";

import { loadBrowserBootstrap } from "@astilba/env/browser";
import { useEffect, useState } from "react";

import { projection } from "../.astilba/env/browser/browser.deployment.ts";

export function BootstrapLabel() {
  const [label, setLabel] = useState("Loading runtime configuration…");
  useEffect(() => {
    let active = true;
    void loadBrowserBootstrap({
      endpoint: "/api/env",
      expectedAudience: { origin: window.location.origin },
      fetch: window.fetch.bind(window),
      projection,
      requestBaseUrl: window.location.href,
    }).then(
      ({ values }) => {
        if (active) {
          setLabel(values.label);
        }
      },
      () => {
        if (active) {
          setLabel("Runtime configuration is unavailable.");
        }
      }
    );
    return () => {
      active = false;
    };
  }, []);
  return <p>{label}</p>;
}
