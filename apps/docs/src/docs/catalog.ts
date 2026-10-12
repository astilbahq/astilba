import type { StarlightUserConfig } from "@astrojs/starlight/types";

import { cacheProduct } from "./products/cache.ts";
import { envProduct } from "./products/env.ts";
import { siteDocsPages } from "./site-pages.ts";
import {
  docsIcons,
  type DocsContext,
  type DocsIcon,
  type DocsPage,
  type DocsProduct,
  type DocsProductContext,
  type DocsVersion,
} from "./types.ts";
import { withDocsBase, withoutDocsBase } from "./urls.ts";

const assertUnique = (
  values: Set<string>,
  value: string,
  description: string
): void => {
  if (values.has(value)) {
    throw new Error(`Duplicate ${description}: "${value}".`);
  }

  values.add(value);
};

const normalizePath = (path: string): string => path.replace(/^\/+|\/+$/g, "");

export const validateDocsProducts = (products: DocsProduct[]): void => {
  const productIds = new Set<string>();
  const productHomePaths = new Set<string>();
  const basePaths = new Set<string>();
  const globalRoutePaths = new Set<string>();
  const globalRoutes = new Map<string, (typeof siteDocsPages)[number]>();
  const routes = new Set<string>();
  const sourcePaths = new Set<string>();

  for (const page of siteDocsPages) {
    const route = normalizePath(withoutDocsBase(page.canonicalPath));
    assertUnique(globalRoutePaths, route, "global documentation route");
    globalRoutes.set(route, page);
    assertUnique(sourcePaths, page.sourcePath, "documentation source path");
  }

  for (const product of products) {
    assertUnique(productIds, product.id, "documentation product id");
    assertUnique(
      productHomePaths,
      normalizePath(product.homePath),
      "documentation product home path"
    );

    if (product.versions.length === 0) {
      throw new Error(`${product.label} must declare at least one version.`);
    }

    const versionIds = new Set<string>();
    let hasDefaultVersion = false;

    for (const version of product.versions) {
      assertUnique(
        versionIds,
        version.id,
        `${product.label} documentation version id`
      );
      if (routes.has(version.basePath)) {
        throw new Error(
          `Documentation base path collides with a page route: "${version.basePath}".`
        );
      }

      const globalRootPage = globalRoutes.get(version.basePath);
      if (globalRootPage && globalRootPage.productId !== product.id) {
        throw new Error(
          `Documentation base path collides with a global page: "${version.basePath}".`
        );
      }

      assertUnique(basePaths, version.basePath, "documentation base path");

      if (version.id === product.defaultVersion) {
        hasDefaultVersion = true;
      }

      const pageKeys = new Set<string>();
      const pageSlugs = new Set<string>();
      let hasDefaultPage = false;

      for (const page of version.sections.flatMap(({ items }) => items)) {
        const route = `${version.basePath}/${page.slug}`;

        if (basePaths.has(route)) {
          throw new Error(
            `Documentation page route collides with a version root: "${route}".`
          );
        }

        if (globalRoutes.has(route)) {
          throw new Error(
            `Documentation page route collides with a global page: "${route}".`
          );
        }

        assertUnique(
          pageKeys,
          page.key,
          `${product.label} ${version.label} page key`
        );
        assertUnique(
          pageSlugs,
          page.slug,
          `${product.label} ${version.label} page slug`
        );
        assertUnique(sourcePaths, page.sourcePath, "documentation source path");
        assertUnique(routes, route, "documentation route");

        if (page.key === product.defaultPage) {
          hasDefaultPage = true;
        }
      }

      if (!hasDefaultPage) {
        throw new Error(
          `Missing default page "${product.defaultPage}" for ${product.label} ${version.label}.`
        );
      }
    }

    if (!hasDefaultVersion) {
      throw new Error(
        `Missing default version "${product.defaultVersion}" for ${product.label}.`
      );
    }

    const productHomePages = siteDocsPages.filter(
      (page) => page.productId === product.id
    );

    if (
      productHomePages.length !== 1 ||
      productHomePages[0].canonicalPath !== withDocsBase(product.homePath)
    ) {
      throw new Error(
        `${product.label} must declare exactly one matching product home page.`
      );
    }
  }

  for (const page of siteDocsPages) {
    if (page.productId && !productIds.has(page.productId)) {
      throw new Error(
        `Unknown documentation product for ${page.canonicalPath}: "${page.productId}".`
      );
    }
  }
};

export const docsProducts: DocsProduct[] = [envProduct, cacheProduct];
validateDocsProducts(docsProducts);

export const docsSidebar: NonNullable<StarlightUserConfig["sidebar"]> =
  docsProducts.map((product) => ({
    label: product.label,
    badge: product.status,
    collapsed: false,
    items: product.versions.map((version) => ({
      label: version.label,
      collapsed: false,
      items: version.sections.map((section) => ({
        label: section.label,
        collapsed: false,
        items: section.items.map((page) => ({
          label: page.label,
          slug: `${version.basePath}/${page.slug}`,
          badge: page.badge,
          attrs: {
            "data-doc-key": page.key,
            "data-nav-icon": page.icon,
            "data-product": product.id,
            "data-version": version.id,
          },
        })),
      })),
    })),
  }));

const docsIconNames = new Set<string>(docsIcons);

export const getPageHref = (version: DocsVersion, page: DocsPage): string =>
  withDocsBase(`/${version.basePath}/${page.slug}/`);

export const getProductHomeHref = (product: DocsProduct): string =>
  withDocsBase(product.homePath);

export const getDefaultVersion = (product: DocsProduct): DocsVersion => {
  const version = product.versions.find(
    ({ id }) => id === product.defaultVersion
  );

  if (!version) {
    throw new Error(
      `Missing default version "${product.defaultVersion}" for ${product.label}.`
    );
  }

  return version;
};

export const getDefaultPage = (
  product: DocsProduct,
  version: DocsVersion
): DocsPage => {
  const page = version.sections
    .flatMap(({ items }) => items)
    .find(({ key }) => key === product.defaultPage);

  if (!page) {
    throw new Error(
      `Missing default page "${product.defaultPage}" for ${product.label} ${version.label}.`
    );
  }

  return page;
};

export const getVersionPageHref = (
  product: DocsProduct,
  version: DocsVersion,
  pageKey: string
): string => {
  const page = version.sections
    .flatMap(({ items }) => items)
    .find(({ key }) => key === pageKey);

  return getPageHref(version, page ?? getDefaultPage(product, version));
};

export const findDocsContext = (pathname: string): DocsContext | undefined => {
  const path = normalizePath(withDocsBase(pathname));

  for (const product of docsProducts) {
    for (const version of product.versions) {
      for (const page of version.sections.flatMap(({ items }) => items)) {
        if (path === normalizePath(getPageHref(version, page))) {
          return { page, product, version };
        }
      }
    }
  }

  return undefined;
};

export const findDocsProductContext = (
  pathname: string
): DocsProductContext | undefined => {
  const pageContext = findDocsContext(pathname);

  if (pageContext) {
    return {
      product: pageContext.product,
      version: pageContext.version,
    };
  }

  const path = normalizePath(withDocsBase(pathname));
  const product = docsProducts.find(
    (candidate) => normalizePath(getProductHomeHref(candidate)) === path
  );

  return product ? { product, version: getDefaultVersion(product) } : undefined;
};

interface DocumentTitleOptions {
  context?: DocsContext;
  isHome: boolean;
  pageTitle: string;
  siteTitle: string;
}

export const getDocumentTitle = ({
  context,
  isHome,
  pageTitle,
  siteTitle,
}: DocumentTitleOptions): string => {
  if (isHome) {
    return siteTitle;
  }

  if (!context) {
    return `${pageTitle} | ${siteTitle}`;
  }

  const { product, version } = context;
  const versionLabel =
    version.id === product.defaultVersion ? "" : ` ${version.label}`;

  return `${pageTitle} | ${siteTitle} ${product.label}${versionLabel}`;
};

export const getVersionMeta = (version: DocsVersion): string => {
  switch (version.lifecycle) {
    case "latest":
      return "Latest";
    case "maintained":
      return "Maintained";
    case "archived":
      return "Archived";
    case "unreleased":
      return "Current";
  }
};

export const getDocsIcon = (value: unknown): DocsIcon | undefined => {
  if (typeof value !== "string" || !docsIconNames.has(value)) {
    return undefined;
  }

  return value as DocsIcon;
};
