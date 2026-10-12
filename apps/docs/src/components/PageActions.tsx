import { Button } from "@astilba/ui/button";
import { Menu } from "@astilba/ui/menu";
import {
  Check,
  ChevronDown,
  CircleAlert,
  Copy,
  ExternalLink,
  Link,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { createPageActionDestinations } from "../docs/page-actions";
import { PageActionBrandIcon } from "./PageActionBrandIcon";
import { pageActionsStyles as styles } from "./PageActions.styles";

type CopyState = "idle" | "copying" | "copied" | "error";
type LinkCopyState = Exclude<CopyState, "copying">;

interface PageActionsProps {
  markdownPath: string;
  sourceUrl: string;
}

const copyText = async (value: string): Promise<void> => {
  if (!navigator.clipboard?.writeText) {
    throw new Error("The Clipboard API is unavailable.");
  }

  await navigator.clipboard.writeText(value);
};

const ActionIcon = ({
  icon: Icon,
  size = 16,
}: {
  icon: LucideIcon;
  size?: number;
}) => (
  <Icon
    absoluteStrokeWidth
    aria-hidden="true"
    className={styles.controlIcon}
    focusable="false"
    size={size}
    strokeWidth={1.25}
  />
);

export const PageActions = ({ markdownPath, sourceUrl }: PageActionsProps) => {
  const [absoluteMarkdownUrl, setAbsoluteMarkdownUrl] = useState(markdownPath);
  const [copyState, setCopyState] = useState<CopyState>("idle");
  const [linkCopyState, setLinkCopyState] = useState<LinkCopyState>("idle");
  const [isReady, setIsReady] = useState(false);
  const [status, setStatus] = useState("");
  const activeCopyFeedback = useRef<
    Extract<CopyState, "copied" | "error"> | undefined
  >(undefined);
  const copyInFlight = useRef(false);
  const copyResetTimer = useRef<number | undefined>(undefined);
  const linkCopyRequestId = useRef(0);
  const linkCopyResetTimer = useRef<number | undefined>(undefined);
  const splitControlRef = useRef<HTMLDivElement>(null);
  const statusResetTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    setIsReady(true);
    setAbsoluteMarkdownUrl(new URL(markdownPath, window.location.href).href);
  }, [markdownPath]);

  useEffect(
    () => () => {
      linkCopyRequestId.current += 1;

      if (copyResetTimer.current !== undefined) {
        window.clearTimeout(copyResetTimer.current);
      }

      if (linkCopyResetTimer.current !== undefined) {
        window.clearTimeout(linkCopyResetTimer.current);
      }

      if (statusResetTimer.current !== undefined) {
        window.clearTimeout(statusResetTimer.current);
      }
    },
    []
  );

  const destinations = useMemo(
    () => createPageActionDestinations(absoluteMarkdownUrl, sourceUrl),
    [absoluteMarkdownUrl, sourceUrl]
  );

  const announceStatus = (message: string): void => {
    if (statusResetTimer.current !== undefined) {
      window.clearTimeout(statusResetTimer.current);
    }

    setStatus(message);
    statusResetTimer.current = window.setTimeout(() => {
      setStatus("");
      statusResetTimer.current = undefined;
    }, 2000);
  };

  const showCopyFeedback = (
    state: Extract<CopyState, "copied" | "error">,
    message: string
  ): void => {
    if (copyResetTimer.current !== undefined) {
      window.clearTimeout(copyResetTimer.current);
    }

    activeCopyFeedback.current = state;
    setCopyState(state);
    announceStatus(message);
    copyResetTimer.current = window.setTimeout(() => {
      activeCopyFeedback.current = undefined;
      setCopyState("idle");
      copyResetTimer.current = undefined;
    }, 2000);
  };

  const copyMarkdown = async (): Promise<void> => {
    if (copyInFlight.current) {
      return;
    }

    if (activeCopyFeedback.current === "copied") {
      showCopyFeedback("copied", "Page Markdown copied.");
      return;
    }

    copyInFlight.current = true;

    if (copyResetTimer.current !== undefined) {
      window.clearTimeout(copyResetTimer.current);
      copyResetTimer.current = undefined;
    }

    activeCopyFeedback.current = undefined;

    if (statusResetTimer.current !== undefined) {
      window.clearTimeout(statusResetTimer.current);
      statusResetTimer.current = undefined;
    }

    setCopyState("copying");
    setStatus("Copying page Markdown.");

    try {
      const response = await fetch(markdownPath, {
        headers: { Accept: "text/markdown" },
      });

      if (!response.ok) {
        throw new Error(`Markdown request failed with ${response.status}.`);
      }

      await copyText(await response.text());
      showCopyFeedback("copied", "Page Markdown copied.");
    } catch {
      showCopyFeedback("error", "Page Markdown could not be copied.");
    } finally {
      copyInFlight.current = false;
    }
  };

  const copyMarkdownLink = async (): Promise<void> => {
    const markdownUrl = new URL(markdownPath, window.location.href).href;
    const requestId = linkCopyRequestId.current + 1;
    linkCopyRequestId.current = requestId;

    if (linkCopyResetTimer.current !== undefined) {
      window.clearTimeout(linkCopyResetTimer.current);
      linkCopyResetTimer.current = undefined;
    }

    let feedback: LinkCopyState = "copied";
    let message = "Markdown link copied.";

    try {
      await copyText(markdownUrl);
    } catch {
      feedback = "error";
      message = "Markdown link could not be copied.";
    }

    if (linkCopyRequestId.current !== requestId) {
      return;
    }

    setLinkCopyState(feedback);
    announceStatus(message);
    linkCopyResetTimer.current = window.setTimeout(() => {
      if (linkCopyRequestId.current === requestId) {
        setLinkCopyState("idle");
        linkCopyResetTimer.current = undefined;
      }
    }, 2000);
  };

  return (
    <div
      aria-label="Page actions"
      className={styles.root}
      data-ready={isReady ? "true" : "false"}
      data-pagefind-ignore=""
      role="group"
    >
      <div
        className={styles.splitControl}
        data-page-actions-split=""
        ref={splitControlRef}
      >
        <Button
          aria-label="Copy Markdown"
          aria-busy={copyState === "copying"}
          aria-disabled={copyState === "copying"}
          className={styles.copyControl}
          data-copy-state={copyState}
          disabled={!isReady}
          onClick={() => {
            void copyMarkdown();
          }}
          title="Copy Markdown"
        >
          <span
            aria-hidden="true"
            className={styles.iconSwap}
            data-state={copyState}
          >
            <span data-copy-icon="idle">
              <ActionIcon icon={Copy} />
            </span>
            <span data-copy-icon="copied">
              <ActionIcon icon={Check} />
            </span>
            <span data-copy-icon="error">
              <ActionIcon icon={CircleAlert} />
            </span>
          </span>
          <span
            aria-hidden="true"
            className={styles.textSwap}
            data-state={copyState}
          >
            <span data-copy-label="idle">Copy Markdown</span>
            <span data-copy-label="copied">Copied!</span>
          </span>
        </Button>

        <Menu.Root modal={false}>
          <Menu.Trigger
            aria-label="More page actions"
            className={styles.menuTrigger}
            disabled={!isReady}
            title="More page actions"
          >
            <ChevronDown
              absoluteStrokeWidth
              aria-hidden="true"
              className={styles.chevron}
              focusable="false"
              size={14}
              strokeWidth={0.75}
            />
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Positioner
              align="start"
              anchor={splitControlRef}
              className={styles.positioner}
              collisionAvoidance={{
                align: "shift",
                fallbackAxisSide: "none",
                side: "shift",
              }}
              collisionPadding={8}
              side="bottom"
              sideOffset={4}
            >
              <Menu.Popup className={styles.menu}>
                <Menu.Item
                  aria-label="Copy Markdown link"
                  className={styles.menuItem}
                  closeOnClick={false}
                  data-copy-state={linkCopyState}
                  data-page-actions-item=""
                  label="Copy Markdown link"
                  onClick={() => {
                    void copyMarkdownLink();
                  }}
                >
                  <span className={styles.menuIcon}>
                    <span
                      aria-hidden="true"
                      className={styles.iconSwap}
                      data-state={linkCopyState}
                    >
                      <span data-copy-icon="idle">
                        <ActionIcon icon={Link} size={14} />
                      </span>
                      <span data-copy-icon="copied">
                        <ActionIcon icon={Check} size={14} />
                      </span>
                      <span data-copy-icon="error">
                        <ActionIcon icon={CircleAlert} size={14} />
                      </span>
                    </span>
                  </span>
                  <Menu.ItemLabel
                    aria-hidden="true"
                    className={styles.textSwap}
                    data-state={linkCopyState}
                  >
                    <span data-copy-label="idle">Copy Markdown link</span>
                    <span data-copy-label="copied">Copied!</span>
                  </Menu.ItemLabel>
                </Menu.Item>

                {destinations.map((destination) => {
                  return (
                    <Menu.LinkItem
                      className={styles.menuItem}
                      closeOnClick
                      data-page-actions-item=""
                      href={destination.href}
                      key={destination.id}
                      label={destination.label}
                      rel="noopener noreferrer"
                      target="_blank"
                    >
                      <span className={styles.menuIcon}>
                        <PageActionBrandIcon
                          className={styles.controlIcon}
                          destination={destination.id}
                          size={14}
                        />
                      </span>
                      <Menu.ItemLabel>{destination.label}</Menu.ItemLabel>
                      <Menu.ItemTrailing
                        aria-hidden="true"
                        className={styles.menuTrailing}
                      >
                        <ActionIcon icon={ExternalLink} size={12} />
                      </Menu.ItemTrailing>
                    </Menu.LinkItem>
                  );
                })}
              </Menu.Popup>
            </Menu.Positioner>
          </Menu.Portal>
        </Menu.Root>
      </div>

      <span aria-live="polite" className={styles.status} role="status">
        {status}
      </span>
    </div>
  );
};
