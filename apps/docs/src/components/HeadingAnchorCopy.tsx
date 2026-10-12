import { Tooltip, TooltipProvider } from "@astilba/ui/tooltip";
import { Link } from "lucide-react";
import { type MouseEvent, useEffect, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

import { headingAnchorCopyStyles as styles } from "./HeadingAnchorCopy.styles";

interface MountedHeadingAnchor {
  host: HTMLSpanElement;
  originalAnchor: HTMLAnchorElement;
  root: Root;
}

interface HeadingAnchorProps {
  headingLabel: string;
  href: string;
}

const HeadingAnchor = ({ headingLabel, href }: HeadingAnchorProps) => {
  const [copied, setCopied] = useState(false);
  const [status, setStatus] = useState("");
  const resetTimer = useRef<number | undefined>(undefined);
  const statusResetTimer = useRef<number | undefined>(undefined);

  useEffect(
    () => () => {
      if (resetTimer.current !== undefined) {
        window.clearTimeout(resetTimer.current);
      }

      if (statusResetTimer.current !== undefined) {
        window.clearTimeout(statusResetTimer.current);
      }
    },
    []
  );

  const announce = (message: string): void => {
    if (statusResetTimer.current !== undefined) {
      window.clearTimeout(statusResetTimer.current);
    }

    setStatus(message);
    statusResetTimer.current = window.setTimeout(() => {
      setStatus("");
      statusResetTimer.current = undefined;
    }, 2000);
  };

  const dismissCopied = (): void => {
    if (resetTimer.current !== undefined) {
      window.clearTimeout(resetTimer.current);
      resetTimer.current = undefined;
    }

    setCopied(false);
  };

  const copyLink = async (
    event: MouseEvent<HTMLAnchorElement>
  ): Promise<void> => {
    if (
      event.button !== 0 ||
      event.altKey ||
      event.ctrlKey ||
      event.metaKey ||
      event.shiftKey ||
      !navigator.clipboard?.writeText
    ) {
      return;
    }

    event.preventDefault();
    dismissCopied();
    const targetUrl = new URL(href, window.location.href).href;

    try {
      await navigator.clipboard.writeText(targetUrl);
      setCopied(true);
      announce(`Link to ${headingLabel} copied.`);

      resetTimer.current = window.setTimeout(() => {
        setCopied(false);
        resetTimer.current = undefined;
      }, 2000);
    } catch {
      announce(`Link to ${headingLabel} could not be copied.`);
      window.location.assign(targetUrl);
    }
  };

  return (
    <>
      <Tooltip
        active={copied}
        activeLabel="Copied!"
        label="Copy link"
        onActiveDismiss={dismissCopied}
      >
        <a
          aria-label={`${copied ? "Copied" : "Copy"} link to ${headingLabel}`}
          className="sl-anchor-link"
          data-tooltip-state={copied ? "active" : "idle"}
          href={href}
          onClick={(event) => {
            void copyLink(event);
          }}
        >
          <span aria-hidden="true" className="sl-anchor-icon">
            <Link
              absoluteStrokeWidth
              aria-hidden="true"
              className={styles.icon}
              focusable="false"
              size={18}
              strokeWidth={1.25}
            />
          </span>
        </a>
      </Tooltip>
      <span
        aria-live="polite"
        className={styles.status}
        data-heading-anchor-status=""
        role="status"
      >
        {status}
      </span>
    </>
  );
};

const HeadingAnchorCopy = () => {
  useEffect(() => {
    const mountedAnchors = [
      ...document.querySelectorAll<HTMLAnchorElement>(
        ".sl-markdown-content .sl-anchor-link"
      ),
    ].map((originalAnchor) => {
      const heading = originalAnchor.parentElement?.querySelector(
        ":scope > h1, :scope > h2, :scope > h3, :scope > h4, :scope > h5, :scope > h6"
      );
      const host = document.createElement("span");

      host.className = styles.host;
      host.dataset.headingAnchorHost = "";
      originalAnchor.replaceWith(host);

      const root = createRoot(host);

      root.render(
        <TooltipProvider>
          <HeadingAnchor
            headingLabel={heading?.textContent?.trim() || "heading"}
            href={originalAnchor.getAttribute("href") || "#"}
          />
        </TooltipProvider>
      );

      return {
        host,
        originalAnchor,
        root,
      };
    }) satisfies MountedHeadingAnchor[];

    return () => {
      for (const { host, originalAnchor, root } of mountedAnchors) {
        root.unmount();

        if (host.isConnected) {
          host.replaceWith(originalAnchor);
        }
      }
    };
  }, []);

  return null;
};

export default HeadingAnchorCopy;
