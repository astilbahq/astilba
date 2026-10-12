import {
  Button,
  Collapsible,
  Field,
  IconButton,
  Input,
  LinkButton,
  Menu,
  ScrollArea,
  Tabs,
  Tooltip,
  TooltipProvider,
  Textarea,
} from "@astilba/ui";
import {
  ArrowRight,
  Check,
  ChevronDown,
  Copy,
  Download,
  ExternalLink,
  Moon,
  Sun,
} from "lucide-react";
import type { MouseEvent as ReactMouseEvent } from "react";
import { useEffect, useRef, useState } from "react";

import lockupLightUrl from "./assets/astilba-lockup-horizontal-light.svg";
import lockupLightSvg from "./assets/astilba-lockup-horizontal-light.svg?raw";
import logomarkColorUrl from "./assets/astilba-logomark-color.svg";
import logomarkColorSvg from "./assets/astilba-logomark-color.svg?raw";
import logomarkDarkUrl from "./assets/astilba-logomark-dark.svg";
import logomarkDarkSvg from "./assets/astilba-logomark-dark.svg?raw";
import logomarkLightUrl from "./assets/astilba-logomark-light.svg";
import logomarkLightSvg from "./assets/astilba-logomark-light.svg?raw";
import wordmarkLightUrl from "./assets/astilba-wordmark-light.svg";
import wordmarkLightSvg from "./assets/astilba-wordmark-light.svg?raw";

type Theme = "dark" | "light";
type BrandCodeFormat = "cdn" | "jsx" | "svg" | "uri";
type BrandAssetId =
  | "lockup-light"
  | "logomark-color"
  | "logomark-dark"
  | "logomark-light"
  | "wordmark-light";
type ShowcasePageId =
  | "actions"
  | "brand"
  | "disclosures"
  | "fields"
  | "icons"
  | "menus"
  | "scroll-areas"
  | "tokens";

interface ShowcasePage {
  description: string;
  href: string;
  id: ShowcasePageId;
  label: string;
  title: string;
}

interface BrandAsset {
  filename: string;
  id: BrandAssetId;
  label: string;
  palette: string;
  previewTone: "dark" | "light" | "neutral";
  svg: string;
  url: string;
}

const COPY_RESET_DELAY = 1400;
const BRAND_COPY_RESET_DELAY = 1600;

const DEFAULT_BRAND_ASSET: BrandAsset = {
  filename: "astilba-logomark-color.svg",
  id: "logomark-color",
  label: "Colour mark",
  palette: "#DF9D86 · #3F6E4E",
  previewTone: "neutral",
  svg: logomarkColorSvg,
  url: logomarkColorUrl,
};

const BRAND_ASSETS: readonly BrandAsset[] = [
  DEFAULT_BRAND_ASSET,
  {
    filename: "astilba-logomark-light.svg",
    id: "logomark-light",
    label: "Light mark",
    palette: "#FBFAF8",
    previewTone: "dark",
    svg: logomarkLightSvg,
    url: logomarkLightUrl,
  },
  {
    filename: "astilba-logomark-dark.svg",
    id: "logomark-dark",
    label: "Dark mark",
    palette: "#131B24",
    previewTone: "light",
    svg: logomarkDarkSvg,
    url: logomarkDarkUrl,
  },
  {
    filename: "astilba-wordmark-light.svg",
    id: "wordmark-light",
    label: "Wordmark",
    palette: "#FFFFFF",
    previewTone: "dark",
    svg: wordmarkLightSvg,
    url: wordmarkLightUrl,
  },
  {
    filename: "astilba-lockup-horizontal-light.svg",
    id: "lockup-light",
    label: "Lockup",
    palette: "#FBFAF8",
    previewTone: "dark",
    svg: lockupLightSvg,
    url: lockupLightUrl,
  },
] as const;

const BRAND_CODE_FORMATS: readonly BrandCodeFormat[] = [
  "svg",
  "jsx",
  "cdn",
  "uri",
];

const SVG_JSX_ATTRIBUTES = [
  ["class", "className"],
  ["clip-path", "clipPath"],
  ["clip-rule", "clipRule"],
  ["fill-rule", "fillRule"],
  ["stroke-linecap", "strokeLinecap"],
  ["stroke-linejoin", "strokeLinejoin"],
  ["stroke-width", "strokeWidth"],
  ["stop-color", "stopColor"],
  ["stop-opacity", "stopOpacity"],
  ["vector-effect", "vectorEffect"],
  ["xlink:href", "xlinkHref"],
  ["xmlns:xlink", "xmlnsXlink"],
] as const;

const isBrandCodeFormat = (value: unknown): value is BrandCodeFormat =>
  BRAND_CODE_FORMATS.some((format) => format === value);

const styleToJsx = (style: string): string => {
  const declarations = style
    .split(";")
    .map((declaration) => declaration.trim())
    .filter(Boolean)
    .map((declaration) => {
      const separator = declaration.indexOf(":");
      if (separator === -1) {
        return null;
      }

      const property = declaration.slice(0, separator).trim();
      const value = declaration.slice(separator + 1).trim();
      if (!(property && value)) {
        return null;
      }

      const jsxProperty = property.startsWith("--")
        ? property
        : property
            .split("-")
            .map((part, index) =>
              index === 0
                ? part
                : `${part[0]?.toUpperCase() ?? ""}${part.slice(1)}`
            )
            .join("");
      return `${JSON.stringify(jsxProperty)}: ${JSON.stringify(value)}`;
    })
    .filter((declaration): declaration is string => declaration !== null);

  return `style={{ ${declarations.join(", ")} }}`;
};

const BRAND_PAGE: ShowcasePage = {
  description:
    "The mark, wordmark, and lockup that identify Astilba across every product.",
  href: "/",
  id: "brand",
  label: "Brand",
  title: "Astilba",
};

const SHOWCASE_PAGES: readonly ShowcasePage[] = [
  BRAND_PAGE,
  {
    description:
      "Buttons and links share one compact hierarchy without losing their native semantics.",
    href: "/actions/",
    id: "actions",
    label: "Actions",
    title: "Actions",
  },
  {
    description:
      "Small, named controls pair restrained iconography with accessible feedback.",
    href: "/icons/",
    id: "icons",
    label: "Icons",
    title: "Icon controls",
  },
  {
    description:
      "Compact surfaces for actions and navigation, with clear pointer and keyboard states.",
    href: "/menus/",
    id: "menus",
    label: "Menus",
    title: "Menus",
  },
  {
    description:
      "Progressively reveal supporting detail without moving focus away from its trigger.",
    href: "/disclosures/",
    id: "disclosures",
    label: "Disclosures",
    title: "Disclosures",
  },
  {
    description:
      "Keep overflow in context with native scrolling, edge fades, and quiet scrollbars.",
    href: "/scroll-areas/",
    id: "scroll-areas",
    label: "Scroll areas",
    title: "Scroll areas",
  },
  {
    description:
      "Labels, guidance, validation, and recovery stay attached to the controls they explain.",
    href: "/fields/",
    id: "fields",
    label: "Fields",
    title: "Form fields",
  },
  {
    description:
      "The semantic colour, type, spacing, and motion decisions beneath every Astilba interface.",
    href: "/tokens/",
    id: "tokens",
    label: "Tokens",
    title: "Semantic base",
  },
] as const;

const pageFromPathname = (pathname: string): ShowcasePage => {
  const firstSegment = pathname.split("/").find(Boolean);
  const normalizedPath = firstSegment ? `/${firstSegment}/` : "/";

  return (
    SHOWCASE_PAGES.find(({ href }) => href === normalizedPath) ?? BRAND_PAGE
  );
};

const removeComments = (node: Node): void => {
  let child = node.firstChild;
  while (child) {
    const next = child.nextSibling;
    if (child.nodeType === 8) {
      child.remove();
    } else {
      removeComments(child);
    }
    child = next;
  }
};

export const stripSvgMetadata = (svg: string): string => {
  const document = new DOMParser().parseFromString(svg, "image/svg+xml");
  const svgElement = document.documentElement;

  if (svgElement.localName !== "svg") {
    throw new Error("The brand asset is not valid SVG.");
  }

  removeComments(svgElement);
  return new XMLSerializer().serializeToString(svgElement).trim();
};

const svgToJsx = (svg: string, componentName: string): string => {
  const withoutMetadata = stripSvgMetadata(svg)
    .replaceAll(/style="(?<style>[^"]*)"/gu, (_match, style: string) =>
      styleToJsx(style)
    )
    .replaceAll(/style='(?<style>[^']*)'/gu, (_match, style: string) =>
      styleToJsx(style)
    );
  let jsx = withoutMetadata;
  for (const [attribute, jsxAttribute] of SVG_JSX_ATTRIBUTES) {
    jsx = jsx.replaceAll(`${attribute}=`, `${jsxAttribute}=`);
  }
  jsx = jsx.trim();

  return `export function ${componentName}() {\n  return (\n${jsx
    .split("\n")
    .map((line) => `    ${line}`)
    .join("\n")}\n  );\n}`;
};

const getBrandCode = (asset: BrandAsset, format: BrandCodeFormat): string => {
  if (format === "jsx") {
    const componentName = asset.id
      .split("-")
      .map((part) => `${part[0]?.toUpperCase() ?? ""}${part.slice(1)}`)
      .join("");
    return svgToJsx(asset.svg, `Astilba${componentName}`);
  }

  if (format === "cdn") {
    return new URL(asset.url, window.location.origin).href;
  }

  const svg = stripSvgMetadata(asset.svg);
  if (format === "uri") {
    return `data:image/svg+xml,${encodeURIComponent(svg)}`;
  }

  return svg;
};

const getSvgDimensions = (svg: string, longestEdge: number) => {
  const viewBox = svg.match(
    /viewBox=["']\s*[-\d.]+\s+[-\d.]+\s+(?<width>[\d.]+)\s+(?<height>[\d.]+)\s*["']/u
  );
  const sourceWidth = Number(viewBox?.groups?.width ?? longestEdge);
  const sourceHeight = Number(viewBox?.groups?.height ?? longestEdge);

  if (sourceWidth >= sourceHeight) {
    return {
      height: Math.max(
        1,
        Math.round((longestEdge * sourceHeight) / sourceWidth)
      ),
      width: longestEdge,
    };
  }

  return {
    height: longestEdge,
    width: Math.max(1, Math.round((longestEdge * sourceWidth) / sourceHeight)),
  };
};

const downloadPng = async (
  asset: BrandAsset,
  longestEdge: number
): Promise<void> => {
  const image = new Image();
  const source = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(
    asset.svg
  )}`;

  image.src = source;
  await image.decode();

  const { height, width } = getSvgDimensions(asset.svg, longestEdge);
  const canvas = document.createElement("canvas");
  canvas.height = height;
  canvas.width = width;
  const context = canvas.getContext("2d");

  if (!context) {
    throw new Error("Canvas is unavailable.");
  }

  context.drawImage(image, 0, 0, width, height);
  const link = document.createElement("a");
  link.download = asset.filename.replace(/\.svg$/u, `-${longestEdge}.png`);
  link.href = canvas.toDataURL("image/png");
  link.click();
};

const ControlArrow = () => (
  <ArrowRight aria-hidden="true" size={14} strokeWidth={1.5} />
);

export const App = () => {
  const [activePage, setActivePage] = useState(() =>
    pageFromPathname(window.location.pathname)
  );
  const [brandCodeCopied, setBrandCodeCopied] = useState(false);
  const [brandCodeError, setBrandCodeError] = useState<string | null>(null);
  const [brandCodeFormat, setBrandCodeFormat] =
    useState<BrandCodeFormat>("svg");
  const [selectedBrandAsset, setSelectedBrandAsset] =
    useState<BrandAsset>(DEFAULT_BRAND_ASSET);
  const [brandExportError, setBrandExportError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [menuAction, setMenuAction] = useState("No action selected");
  const [theme, setTheme] = useState<Theme>("dark");
  const brandCopyResetTimer = useRef<number | null>(null);
  const copyResetTimer = useRef<number | null>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    document.title = `${activePage.title} · Astilba Interface`;
  }, [activePage.title]);

  useEffect(() => {
    const handlePopState = () => {
      setActivePage(pageFromPathname(window.location.pathname));
    };

    window.addEventListener("popstate", handlePopState);
    return () => {
      window.removeEventListener("popstate", handlePopState);
    };
  }, []);

  useEffect(
    () => () => {
      if (brandCopyResetTimer.current !== null) {
        window.clearTimeout(brandCopyResetTimer.current);
      }
      if (copyResetTimer.current !== null) {
        window.clearTimeout(copyResetTimer.current);
      }
    },
    []
  );

  const isDark = theme === "dark";
  const copyBrandCode = async (code: string): Promise<void> => {
    try {
      await navigator.clipboard.writeText(code);
    } catch {
      setBrandCodeCopied(false);
      setBrandCodeError("Code could not be copied.");
      return;
    }

    setBrandCodeError(null);
    setBrandCodeCopied(true);
    if (brandCopyResetTimer.current !== null) {
      window.clearTimeout(brandCopyResetTimer.current);
    }
    brandCopyResetTimer.current = window.setTimeout(() => {
      setBrandCodeCopied(false);
    }, BRAND_COPY_RESET_DELAY);
  };
  const exportBrandPng = async (
    asset: BrandAsset,
    longestEdge: number
  ): Promise<void> => {
    try {
      await downloadPng(asset, longestEdge);
      setBrandExportError(null);
    } catch {
      setBrandExportError("PNG could not be exported.");
    }
  };
  const navigate = (
    event: ReactMouseEvent<HTMLAnchorElement>,
    page: ShowcasePage
  ) => {
    if (
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return;
    }

    event.preventDefault();
    if (page.id === activePage.id) {
      if (window.scrollY !== 0) {
        window.scrollTo({ top: 0 });
      }
      return;
    }

    window.history.pushState({}, "", page.href);
    setActivePage(page);
    if (window.scrollY !== 0) {
      window.scrollTo({ top: 0 });
    }
  };

  return (
    <TooltipProvider>
      <a className="skip-link" href="#showcase-content">
        Skip to content
      </a>

      <header className="showcase-header">
        <a
          className="showcase-brand"
          href="https://astilba.com/"
          aria-label="Astilba home"
        >
          <span>Astilba</span>
          <span aria-hidden="true">/</span>
          <span>Interface</span>
        </a>

        <Tooltip label={isDark ? "Use light theme" : "Use dark theme"}>
          <IconButton
            label={isDark ? "Use light theme" : "Use dark theme"}
            onClick={() => {
              setTheme(isDark ? "light" : "dark");
            }}
          >
            {isDark ? (
              <Sun aria-hidden="true" size={16} strokeWidth={1.5} />
            ) : (
              <Moon aria-hidden="true" size={16} strokeWidth={1.5} />
            )}
          </IconButton>
        </Tooltip>
      </header>

      <nav className="showcase-nav" aria-label="Interface sections">
        <div>
          {SHOWCASE_PAGES.map((page) => (
            <a
              aria-current={activePage.id === page.id ? "page" : undefined}
              href={page.href}
              key={page.id}
              onClick={(event) => {
                navigate(event, page);
              }}
            >
              {page.label}
            </a>
          ))}
        </div>
      </nav>

      <main id="showcase-content" tabIndex={-1}>
        <section className="showcase-intro" aria-labelledby="showcase-title">
          <p className="showcase-kicker">Interface / {activePage.label}</p>
          <h1 id="showcase-title">{activePage.title}</h1>
          <p>{activePage.description}</p>
        </section>

        <section
          className="specimen-section brand-section"
          aria-labelledby="brand-title"
          hidden={activePage.id !== "brand"}
        >
          <div className="brand-workbench">
            <div className="brand-preview-column">
              <figure
                className="brand-preview"
                data-tone={selectedBrandAsset.previewTone}
              >
                <figcaption>{selectedBrandAsset.palette}</figcaption>
                <img
                  alt={`${selectedBrandAsset.label} preview`}
                  src={selectedBrandAsset.url}
                />
              </figure>
            </div>

            <div className="brand-details">
              <div className="brand-details-header">
                <h2 id="brand-title">{selectedBrandAsset.label}</h2>
                <div className="brand-download">
                  <a
                    className="brand-download-primary"
                    download={selectedBrandAsset.filename}
                    href={selectedBrandAsset.url}
                  >
                    <Download aria-hidden="true" size={14} strokeWidth={1.5} />
                    Download SVG
                  </a>
                  <Menu.Root>
                    <Menu.Trigger
                      aria-label="More download formats"
                      className="brand-download-trigger"
                    >
                      <ChevronDown
                        aria-hidden="true"
                        size={14}
                        strokeWidth={1.5}
                      />
                    </Menu.Trigger>
                    <Menu.Portal>
                      <Menu.Positioner align="end">
                        <Menu.Popup className="showcase-menu-popup">
                          <Menu.LinkItem
                            download={selectedBrandAsset.filename}
                            href={selectedBrandAsset.url}
                            label="Download SVG"
                          >
                            <Menu.ItemLabel>Download SVG</Menu.ItemLabel>
                            <Menu.ItemTrailing>Vector</Menu.ItemTrailing>
                          </Menu.LinkItem>
                          <Menu.Item
                            label="Download PNG"
                            onClick={async () => {
                              await exportBrandPng(selectedBrandAsset, 512);
                            }}
                          >
                            <Menu.ItemLabel>Download PNG</Menu.ItemLabel>
                            <Menu.ItemTrailing>512px</Menu.ItemTrailing>
                          </Menu.Item>
                        </Menu.Popup>
                      </Menu.Positioner>
                    </Menu.Portal>
                  </Menu.Root>
                </div>
              </div>

              <div className="brand-variant-section">
                <p>Variants</p>
                <div className="brand-variants">
                  {BRAND_ASSETS.map((asset) => (
                    <button
                      aria-pressed={asset.id === selectedBrandAsset.id}
                      key={asset.id}
                      onClick={() => {
                        setSelectedBrandAsset(asset);
                        setBrandCodeCopied(false);
                        setBrandCodeError(null);
                        setBrandExportError(null);
                      }}
                      type="button"
                    >
                      <span
                        className="brand-variant-preview"
                        data-tone={asset.previewTone}
                      >
                        <img alt="" src={asset.url} />
                      </span>
                      <span>{asset.label}</span>
                    </button>
                  ))}
                </div>
              </div>

              <Tabs.Root
                className="brand-code"
                onValueChange={(value) => {
                  if (!isBrandCodeFormat(value)) {
                    return;
                  }
                  setBrandCodeFormat(value);
                  setBrandCodeCopied(false);
                  setBrandCodeError(null);
                }}
                value={brandCodeFormat}
              >
                <div className="brand-code-toolbar">
                  <Tabs.List
                    activateOnFocus
                    aria-label="Asset code formats"
                    className="brand-code-tabs"
                  >
                    {BRAND_CODE_FORMATS.map((format) => (
                      <Tabs.Tab key={format} value={format}>
                        {format.toUpperCase()}
                      </Tabs.Tab>
                    ))}
                    <Tabs.Indicator />
                  </Tabs.List>
                  <Tooltip
                    active={brandCodeCopied}
                    activeLabel="Copied!"
                    label="Copy code"
                    onActiveDismiss={() => {
                      setBrandCodeCopied(false);
                    }}
                  >
                    <IconButton
                      appearance="ghost"
                      className="brand-code-copy"
                      label="Copy code"
                      onClick={() =>
                        copyBrandCode(
                          getBrandCode(selectedBrandAsset, brandCodeFormat)
                        )
                      }
                    >
                      {brandCodeCopied ? (
                        <Check aria-hidden="true" size={16} strokeWidth={1.5} />
                      ) : (
                        <Copy aria-hidden="true" size={16} strokeWidth={1.5} />
                      )}
                    </IconButton>
                  </Tooltip>
                </div>
                {BRAND_CODE_FORMATS.map((format) => {
                  const code = getBrandCode(selectedBrandAsset, format);

                  return (
                    <Tabs.Panel
                      className="brand-code-panel"
                      keepMounted
                      key={format}
                      value={format}
                    >
                      <ScrollArea.Root className="brand-code-scroll-area">
                        <ScrollArea.Viewport
                          aria-label={`${format.toUpperCase()} source code`}
                          className="brand-code-scroll-viewport"
                        >
                          <ScrollArea.Content className="brand-code-scroll-content">
                            <pre>
                              <code>{code}</code>
                            </pre>
                          </ScrollArea.Content>
                        </ScrollArea.Viewport>
                        <ScrollArea.Scrollbar>
                          <ScrollArea.Thumb />
                        </ScrollArea.Scrollbar>
                        <ScrollArea.Scrollbar orientation="horizontal">
                          <ScrollArea.Thumb />
                        </ScrollArea.Scrollbar>
                        <ScrollArea.Corner />
                      </ScrollArea.Root>
                    </Tabs.Panel>
                  );
                })}
              </Tabs.Root>
              <p aria-live="polite" className="brand-code-status">
                {brandCodeError}
              </p>

              <div className="brand-export">
                <p>Export PNG</p>
                <div>
                  {[32, 64, 128, 256, 512].map((size) => (
                    <Button
                      key={size}
                      onClick={async () => {
                        await exportBrandPng(selectedBrandAsset, size);
                      }}
                    >
                      {size}px
                    </Button>
                  ))}
                </div>
                <p aria-live="polite" className="brand-export-status">
                  {brandExportError}
                </p>
              </div>
            </div>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="actions-title"
          hidden={activePage.id !== "actions"}
        >
          <div className="section-heading">
            <div>
              <p>01</p>
              <h2 id="actions-title">Actions</h2>
            </div>
            <code>Button · LinkButton</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Hierarchy</h3>
              <p>Three appearances, one quiet interaction language.</p>
            </div>
            <div className="control-line">
              <Button appearance="primary">
                Continue
                <ControlArrow />
              </Button>
              <Button appearance="outline">Review</Button>
              <Button>Dismiss</Button>
            </div>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Navigation</h3>
              <p>
                Links keep anchor semantics even when they look like controls.
              </p>
            </div>
            <div className="control-line">
              <LinkButton appearance="primary" href="/tokens/" size="large">
                Read the contract
                <ControlArrow />
              </LinkButton>
              <LinkButton appearance="outline" href="https://astilba.com">
                Astilba.com
              </LinkButton>
            </div>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Unavailable</h3>
              <p>Disabled controls remain legible without suggesting action.</p>
            </div>
            <div className="control-line">
              <Button appearance="primary" disabled>
                Not available
              </Button>
              <Button disabled>Not available</Button>
              <Button appearance="outline" disabled>
                Not available
              </Button>
            </div>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Scale</h3>
              <p>
                Large controls keep the same hierarchy with a roomier
                interaction target.
              </p>
            </div>
            <div className="control-line">
              <Button appearance="primary" size="large">
                Continue large
                <ControlArrow />
              </Button>
              <IconButton label="Large icon control" size="large">
                <Sun aria-hidden="true" size={18} strokeWidth={1.5} />
              </IconButton>
            </div>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="icons-title"
          hidden={activePage.id !== "icons"}
        >
          <div className="section-heading">
            <div>
              <p>Feedback</p>
              <h2 id="icons-title">Icon buttons and tooltips</h2>
            </div>
            <code>IconButton · Tooltip</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Named, not labelled</h3>
              <p>
                The visible icon stays small; its accessible name does the
                semantic work.
              </p>
            </div>
            <div className="control-line">
              <Tooltip
                active={copied}
                activeLabel="Copied!"
                label="Copy"
                onActiveDismiss={() => {
                  setCopied(false);
                }}
              >
                <IconButton
                  label="Copy"
                  onClick={() => {
                    setCopied(true);
                    if (copyResetTimer.current !== null) {
                      window.clearTimeout(copyResetTimer.current);
                    }
                    copyResetTimer.current = window.setTimeout(() => {
                      setCopied(false);
                    }, COPY_RESET_DELAY);
                  }}
                >
                  {copied ? (
                    <Check aria-hidden="true" size={16} strokeWidth={1.5} />
                  ) : (
                    <Copy aria-hidden="true" size={16} strokeWidth={1.5} />
                  )}
                </IconButton>
              </Tooltip>
              <Tooltip label={isDark ? "Use light theme" : "Use dark theme"}>
                <IconButton
                  label={isDark ? "Use light theme" : "Use dark theme"}
                  onClick={() => {
                    setTheme(isDark ? "light" : "dark");
                  }}
                >
                  {isDark ? (
                    <Sun aria-hidden="true" size={16} strokeWidth={1.5} />
                  ) : (
                    <Moon aria-hidden="true" size={16} strokeWidth={1.5} />
                  )}
                </IconButton>
              </Tooltip>
            </div>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="menus-title"
          hidden={activePage.id !== "menus"}
        >
          <div className="section-heading">
            <div>
              <p>Overlays</p>
              <h2 id="menus-title">Menu primitives</h2>
            </div>
            <code>Menu</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Actions and navigation</h3>
              <p>
                Pointer hover stays quiet while keyboard movement receives an
                unmistakable focus signal.
              </p>
            </div>
            <div>
              <div className="control-line">
                <Menu.Root>
                  <Menu.Trigger className="showcase-menu-trigger">
                    More actions
                    <ChevronDown
                      aria-hidden="true"
                      size={14}
                      strokeWidth={1.5}
                    />
                  </Menu.Trigger>
                  <Menu.Portal>
                    <Menu.Positioner>
                      <Menu.Popup className="showcase-menu-popup">
                        <Menu.Item
                          label="Update project"
                          onClick={() => {
                            setMenuAction("Project updated");
                          }}
                        >
                          <Menu.ItemLabel>Update project</Menu.ItemLabel>
                        </Menu.Item>
                        <Menu.Item
                          closeOnClick={false}
                          label="Keep menu open"
                          onClick={() => {
                            setMenuAction("Menu kept open");
                          }}
                        >
                          <Menu.ItemLabel>Keep menu open</Menu.ItemLabel>
                        </Menu.Item>
                        <Menu.Item disabled label="Archived action">
                          <Menu.ItemLabel>Archived action</Menu.ItemLabel>
                        </Menu.Item>
                        <Menu.LinkItem href="/tokens/" label="View tokens">
                          <Menu.ItemLabel>View tokens</Menu.ItemLabel>
                          <Menu.ItemTrailing aria-hidden="true">
                            <ExternalLink size={12} strokeWidth={1.5} />
                          </Menu.ItemTrailing>
                        </Menu.LinkItem>
                      </Menu.Popup>
                    </Menu.Positioner>
                  </Menu.Portal>
                </Menu.Root>
              </div>
              <p className="showcase-status" aria-live="polite">
                {menuAction}
              </p>
            </div>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="disclosures-title"
          hidden={activePage.id !== "disclosures"}
        >
          <div className="section-heading">
            <div>
              <p>Behaviour</p>
              <h2 id="disclosures-title">Collapsible content</h2>
            </div>
            <code>Collapsible</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Details in context</h3>
              <p>
                A disclosure explains its state without moving focus away from
                the decision that opened it.
              </p>
            </div>
            <Collapsible.Root className="showcase-collapsible">
              <Collapsible.Trigger className="showcase-collapsible-trigger">
                Deployment details
                <ChevronDown
                  aria-hidden="true"
                  className="showcase-collapsible-chevron"
                  size={14}
                  strokeWidth={1.5}
                />
              </Collapsible.Trigger>
              <Collapsible.Panel className="showcase-collapsible-panel">
                <p>
                  The release uses a verified artifact and retains native button
                  semantics.
                </p>
              </Collapsible.Panel>
            </Collapsible.Root>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="scroll-title"
          hidden={activePage.id !== "scroll-areas"}
        >
          <div className="section-heading">
            <div>
              <p>Overflow</p>
              <h2 id="scroll-title">Contained scrolling</h2>
            </div>
            <code>ScrollArea</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Overflow in context</h3>
              <p>
                Edge fades explain position while the quiet scrollbar appears
                for pointer, keyboard, and active scrolling.
              </p>
            </div>
            <ScrollArea.Root
              className="showcase-scroll-area"
              data-testid="vertical-scroll-area"
            >
              <ScrollArea.Viewport
                className="showcase-scroll-area-viewport"
                data-testid="vertical-scroll-viewport"
                fade="block"
              >
                <ScrollArea.Content className="showcase-scroll-area-content">
                  {[
                    "Prepare release",
                    "Verify artifact",
                    "Publish package",
                    "Confirm provenance",
                    "Deploy consumer",
                    "Run smoke checks",
                    "Observe production",
                    "Record outcome",
                  ].map((step, index) => (
                    <a href={`#scroll-step-${index + 1}`} key={step}>
                      <span>{String(index + 1).padStart(2, "0")}</span>
                      {step}
                    </a>
                  ))}
                </ScrollArea.Content>
              </ScrollArea.Viewport>
              <ScrollArea.Scrollbar data-testid="vertical-scrollbar">
                <ScrollArea.Thumb />
              </ScrollArea.Scrollbar>
            </ScrollArea.Root>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Either axis</h3>
              <p>
                The same primitive supports wide content without changing its
                native scroll behaviour.
              </p>
            </div>
            <ScrollArea.Root
              className="showcase-scroll-area showcase-scroll-area--horizontal"
              data-testid="horizontal-scroll-area"
              direction="rtl"
            >
              <ScrollArea.Viewport
                className="showcase-scroll-area-viewport"
                data-testid="horizontal-scroll-viewport"
              >
                <ScrollArea.Content
                  className="showcase-scroll-area-strip"
                  dir="ltr"
                >
                  {[
                    "Node",
                    "Bun",
                    "Cloudflare Workers",
                    "React Router",
                    "Hono",
                  ].map((runtime) => (
                    <span key={runtime}>{runtime}</span>
                  ))}
                </ScrollArea.Content>
              </ScrollArea.Viewport>
              <ScrollArea.Scrollbar
                data-testid="horizontal-scrollbar"
                orientation="horizontal"
              >
                <ScrollArea.Thumb />
              </ScrollArea.Scrollbar>
            </ScrollArea.Root>
          </div>
        </section>

        <section
          className="specimen-section"
          aria-labelledby="fields-title"
          hidden={activePage.id !== "fields"}
        >
          <div className="section-heading">
            <div>
              <p>Inputs</p>
              <h2 id="fields-title">Field foundations</h2>
            </div>
            <code>Field · Input · Textarea</code>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Guidance in place</h3>
              <p>
                Labels and supporting copy stay attached to the control they
                explain.
              </p>
            </div>
            <div className="showcase-form-stack">
              <Field.Root>
                <Field.Label htmlFor="showcase-project">
                  Project name
                </Field.Label>
                <Input
                  aria-describedby="showcase-project-help"
                  defaultValue="astilba-app"
                  id="showcase-project"
                  name="showcaseProject"
                />
                <Field.Description id="showcase-project-help">
                  Used for the package and repository name.
                </Field.Description>
              </Field.Root>
              <Field.Root>
                <Field.Label htmlFor="showcase-description">
                  Description
                </Field.Label>
                <Textarea
                  defaultValue="A focused TypeScript project."
                  id="showcase-description"
                  name="showcaseDescription"
                  rows={3}
                />
              </Field.Root>
            </div>
          </div>

          <div className="specimen-row">
            <div>
              <h3>Clear recovery</h3>
              <p>
                Invalid and unavailable fields explain state without changing
                the layout.
              </p>
            </div>
            <div className="showcase-form-stack">
              <Field.Root data-invalid="">
                <Field.Label htmlFor="showcase-owner">GitHub owner</Field.Label>
                <Input
                  aria-describedby="showcase-owner-error"
                  aria-invalid="true"
                  defaultValue="invalid--owner"
                  data-testid="invalid-input"
                  id="showcase-owner"
                  name="showcaseOwner"
                />
                <Field.Error id="showcase-owner-error">
                  Use a valid GitHub account name.
                </Field.Error>
              </Field.Root>
              <Field.Root>
                <Field.Label htmlFor="showcase-disabled">
                  Generated identifier
                </Field.Label>
                <Input
                  defaultValue="Assigned after creation"
                  disabled
                  id="showcase-disabled"
                  name="showcaseDisabled"
                />
              </Field.Root>
            </div>
          </div>
        </section>

        <section
          className="specimen-section token-section"
          id="tokens"
          aria-labelledby="tokens-title"
          hidden={activePage.id !== "tokens"}
        >
          <div className="section-heading">
            <div>
              <p>Foundation</p>
              <h2 id="tokens-title">Colour roles</h2>
            </div>
            <code>@astilba/tokens</code>
          </div>

          <dl className="token-list">
            <div>
              <dt>Canvas</dt>
              <dd>
                <span className="token-swatch token-swatch--canvas" />
                <code>colors.canvas</code>
              </dd>
            </div>
            <div>
              <dt>Ink</dt>
              <dd>
                <span className="token-swatch token-swatch--ink" />
                <code>colors.ink.default</code>
              </dd>
            </div>
            <div>
              <dt>Signal</dt>
              <dd>
                <span className="token-swatch token-swatch--signal" />
                <code>colors.signal</code>
              </dd>
            </div>
            <div>
              <dt>Control border</dt>
              <dd>
                <span className="token-swatch token-swatch--border" />
                <code>colors.border.control</code>
              </dd>
            </div>
          </dl>
        </section>
      </main>

      <footer className="showcase-footer">
        <p>Private test harness · public packages</p>
        <p>Astilba interface foundation</p>
      </footer>
    </TooltipProvider>
  );
};
