import {
  defineRouteMiddleware,
  type StarlightRouteData,
} from "@astrojs/starlight/route-data";

import { findDocsProductContext } from "./catalog";

type SidebarEntry = StarlightRouteData["sidebar"][number];
type SidebarGroup = Extract<SidebarEntry, { type: "group" }>;
type SidebarLink = Exclude<StarlightRouteData["pagination"]["prev"], undefined>;
type PrevNextConfig = StarlightRouteData["entry"]["data"]["prev"];

const isGroup = (entry: SidebarEntry): entry is SidebarGroup =>
  entry.type === "group";

const containsCurrentPage = (entry: SidebarEntry): boolean =>
  entry.type === "link"
    ? entry.isCurrent
    : entry.entries.some(containsCurrentPage);

const containsLinkAttribute = (
  entry: SidebarEntry,
  attribute: "data-product" | "data-version",
  value: string
): boolean =>
  entry.type === "link"
    ? Object.entries(entry.attrs).some(
        ([name, attributeValue]) =>
          name === attribute && attributeValue === value
      )
    : entry.entries.some((child) =>
        containsLinkAttribute(child, attribute, value)
      );

const flattenSidebar = (entries: SidebarEntry[]): SidebarLink[] =>
  entries.flatMap((entry) =>
    entry.type === "group" ? flattenSidebar(entry.entries) : entry
  );

const applyLinkConfig = (
  link: SidebarLink | undefined,
  config: PrevNextConfig
): SidebarLink | undefined => {
  if (config === false) {
    return undefined;
  }

  if (config === undefined || config === true) {
    return link;
  }

  if (typeof config === "string") {
    return link ? { ...link, label: config } : undefined;
  }

  if (link) {
    return {
      ...link,
      label: config.label ?? link.label,
      href: config.link ?? link.href,
      attrs: {},
    };
  }

  if (config.link && config.label) {
    return {
      type: "link",
      label: config.label,
      href: config.link,
      isCurrent: false,
      badge: undefined,
      attrs: {},
    };
  }

  return undefined;
};

const updatePagination = (route: StarlightRouteData): void => {
  const links = flattenSidebar(route.sidebar);
  const currentIndex = links.findIndex(({ isCurrent }) => isCurrent);
  const previous = currentIndex > 0 ? links[currentIndex - 1] : undefined;
  const next = currentIndex >= 0 ? links[currentIndex + 1] : undefined;

  route.pagination = {
    prev: applyLinkConfig(previous, route.entry.data.prev),
    next: applyLinkConfig(next, route.entry.data.next),
  };
};

export const onRequest = defineRouteMiddleware((context, next) => {
  const route = context.locals.starlightRoute;
  const productContext = findDocsProductContext(context.url.pathname);
  const productGroup = route.sidebar.find(
    (entry): entry is SidebarGroup =>
      isGroup(entry) &&
      (containsCurrentPage(entry) ||
        (productContext !== undefined &&
          containsLinkAttribute(
            entry,
            "data-product",
            productContext.product.id
          )))
  );
  const versionGroup = productGroup?.entries.find(
    (entry): entry is SidebarGroup =>
      isGroup(entry) &&
      (containsCurrentPage(entry) ||
        (productContext !== undefined &&
          containsLinkAttribute(
            entry,
            "data-version",
            productContext.version.id
          )))
  );

  if (versionGroup) {
    route.sidebar = versionGroup.entries;
    updatePagination(route);
  } else {
    route.sidebar = [];
    updatePagination(route);
  }

  return next();
});
