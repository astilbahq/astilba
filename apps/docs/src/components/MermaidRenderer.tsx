import { Tooltip, TooltipProvider } from "@astilba/ui/tooltip";
import { Check, CircleAlert, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { mermaidRendererStyles as styles } from "./MermaidRenderer.styles";

const selector = "[data-mermaid]";
const sourceAttribute = "data-mermaid-source";
const headingSelector = "h1, h2, h3, h4, h5, h6";
const copyFeedbackDuration = 2000;

type CopyState = "copied" | "error" | "idle";

interface RenderedDiagram {
  readonly host: HTMLSpanElement;
  readonly id: string;
  readonly label: string;
  readonly source: string;
}

const findDiagramLabel = (node: HTMLElement, index: number): string => {
  let sibling = node.previousElementSibling;

  while (sibling) {
    const heading = sibling.matches(headingSelector)
      ? sibling
      : sibling.querySelector(headingSelector);
    const label = heading?.textContent?.trim();

    if (label) {
      return label;
    }

    sibling = sibling.previousElementSibling;
  }

  return `Diagram ${index + 1}`;
};

const readTheme = (): "dark" | "default" =>
  document.documentElement.dataset.theme === "dark" ? "dark" : "default";

const CopyDiagramSourceButton = ({
  label,
  source,
}: {
  readonly label: string;
  readonly source: string;
}) => {
  const [copyState, setCopyState] = useState<CopyState>("idle");
  const [status, setStatus] = useState("");
  const requestId = useRef(0);
  const resetTimer = useRef<number | undefined>(undefined);

  useEffect(
    () => () => {
      requestId.current += 1;

      if (resetTimer.current !== undefined) {
        window.clearTimeout(resetTimer.current);
      }
    },
    []
  );

  const dismissFeedback = (): void => {
    requestId.current += 1;

    if (resetTimer.current !== undefined) {
      window.clearTimeout(resetTimer.current);
      resetTimer.current = undefined;
    }

    setCopyState("idle");
    setStatus("");
  };

  const showFeedback = (
    state: Exclude<CopyState, "idle">,
    message: string,
    activeRequestId: number
  ): void => {
    setCopyState(state);
    setStatus(message);
    resetTimer.current = window.setTimeout(() => {
      if (requestId.current !== activeRequestId) {
        return;
      }

      setCopyState("idle");
      setStatus("");
      resetTimer.current = undefined;
    }, copyFeedbackDuration);
  };

  const copySource = async (): Promise<void> => {
    dismissFeedback();
    const activeRequestId = requestId.current;

    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("The Clipboard API is unavailable.");
      }

      await navigator.clipboard.writeText(source);

      if (requestId.current === activeRequestId) {
        showFeedback(
          "copied",
          `${label} Mermaid source copied.`,
          activeRequestId
        );
      }
    } catch {
      if (requestId.current === activeRequestId) {
        showFeedback(
          "error",
          `${label} Mermaid source could not be copied.`,
          activeRequestId
        );
      }
    }
  };

  const action =
    copyState === "copied"
      ? "Copied"
      : copyState === "error"
        ? "Copy failed"
        : "Copy";

  return (
    <>
      <Tooltip
        active={copyState !== "idle"}
        activeLabel={copyState === "error" ? "Copy failed" : "Copied!"}
        label="Copy Mermaid source"
        onActiveDismiss={dismissFeedback}
      >
        <button
          aria-label={`${action} Mermaid source for ${label}`}
          className={styles.action}
          data-copy-state={copyState}
          onClick={() => {
            void copySource();
          }}
          type="button"
        >
          <span
            aria-hidden="true"
            className={styles.iconSwap}
            data-state={copyState}
          >
            <span data-copy-icon="idle">
              <Copy
                absoluteStrokeWidth
                className={styles.actionIcon}
                size={16}
                strokeWidth={1.25}
              />
            </span>
            <span data-copy-icon="copied">
              <Check
                absoluteStrokeWidth
                className={styles.actionIcon}
                size={16}
                strokeWidth={1.25}
              />
            </span>
            <span data-copy-icon="error">
              <CircleAlert
                absoluteStrokeWidth
                className={styles.actionIcon}
                size={16}
                strokeWidth={1.25}
              />
            </span>
          </span>
        </button>
      </Tooltip>
      <span aria-live="polite" className={styles.visuallyHidden} role="status">
        {status}
      </span>
    </>
  );
};

export const MermaidRenderer = () => {
  const [diagrams, setDiagrams] = useState<RenderedDiagram[]>([]);

  useEffect(() => {
    let cancelled = false;
    let generation = 0;
    const actionHosts = new Map<HTMLElement, HTMLSpanElement>();

    const render = async (): Promise<void> => {
      generation += 1;
      const pass = generation;
      const isStale = (): boolean => cancelled || pass !== generation;
      const targets = document.querySelectorAll<HTMLElement>(selector);

      if (targets.length === 0) {
        return;
      }

      const { default: mermaid } = await import("mermaid");

      if (isStale()) {
        return;
      }

      mermaid.initialize({
        fontFamily: "var(--sl-font, sans-serif)",
        securityLevel: "strict",
        startOnLoad: false,
        theme: readTheme(),
      });

      const nextDiagrams: RenderedDiagram[] = [];

      for (const [index, node] of [...targets].entries()) {
        const source =
          node.getAttribute(sourceAttribute) ?? (node.textContent ?? "").trim();

        if (!source) {
          continue;
        }

        node.setAttribute(sourceAttribute, source);

        try {
          const { svg } = await mermaid.render(
            `mermaid-${pass}-${index}`,
            source
          );

          if (isStale()) {
            return;
          }

          const host = actionHosts.get(node) ?? document.createElement("span");
          actionHosts.set(node, host);
          host.className = `${styles.actions} not-content`;
          node.innerHTML = svg;
          const renderedSvg = node.querySelector("svg");

          if (!renderedSvg) {
            node.dataset.mermaidError = "";
            continue;
          }

          node.replaceChildren(renderedSvg, host);
          node.classList.add(styles.enhancedDiagram);
          node.dataset.mermaidRendered = "";
          delete node.dataset.mermaidError;
          nextDiagrams.push({
            host,
            id: `mermaid-${index}`,
            label: findDiagramLabel(node, index),
            source,
          });
        } catch {
          if (isStale()) {
            return;
          }

          node.dataset.mermaidError = "";
        }
      }

      if (!isStale()) {
        setDiagrams(nextDiagrams);
      }
    };

    void render();

    const themeObserver = new MutationObserver(() => {
      void render();
    });
    themeObserver.observe(document.documentElement, {
      attributeFilter: ["data-theme"],
      attributes: true,
    });

    return () => {
      cancelled = true;
      themeObserver.disconnect();

      for (const host of actionHosts.values()) {
        host.remove();
      }
    };
  }, []);

  return (
    <TooltipProvider>
      {diagrams.map(({ host, id, label, source }) =>
        createPortal(
          <CopyDiagramSourceButton label={label} source={source} />,
          host,
          id
        )
      )}
    </TooltipProvider>
  );
};
