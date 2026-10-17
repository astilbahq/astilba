import type { APIRoute, GetStaticPaths } from "astro";
import { getCollection } from "astro:content";

import { docsProducts, findDocsContext } from "../docs/catalog";
import { findSiteDocsPage } from "../docs/site-pages";
import { getDocsSourceUrl } from "../docs/source";
import { withDocsBase } from "../docs/urls";

interface MarkdownPageProps {
  body: string | undefined;
  canonicalPath: string;
  description: string;
  docsVersion?: string;
  docsVersionId?: string;
  lifecycle?: string;
  product?: string;
  productId?: string;
  source: string;
  title: string;
}

export const prerender = true;

const yamlString = (value: string): string => JSON.stringify(value);

const createMarkdown = (
  props: MarkdownPageProps,
  canonical: string
): string => {
  const productFrontmatter = props.product
    ? [
        `product: ${yamlString(props.product)}`,
        `productId: ${yamlString(props.productId ?? "")}`,
        ...(props.docsVersion
          ? [
              `docsVersion: ${yamlString(props.docsVersion)}`,
              `docsVersionId: ${yamlString(props.docsVersionId ?? "")}`,
              `lifecycle: ${yamlString(props.lifecycle ?? "")}`,
            ]
          : []),
      ]
    : [];
  const frontmatter = [
    "---",
    `title: ${yamlString(props.title)}`,
    `description: ${yamlString(props.description)}`,
    `canonical: ${yamlString(canonical)}`,
    ...productFrontmatter,
    `source: ${yamlString(props.source)}`,
    "---",
  ].join("\n");

  return (
    [
      frontmatter,
      `# ${props.title}`,
      props.description ? `> ${props.description}` : undefined,
      props.body?.trim(),
    ]
      .filter((section): section is string => Boolean(section))
      .join("\n\n") + "\n"
  );
};

export const getStaticPaths = (async () => {
  const entries = await getCollection(
    "docs",
    ({ data }) => import.meta.env.MODE !== "production" || data.draft === false
  );

  return entries.flatMap((entry) => {
    const sitePage = findSiteDocsPage(entry.id);

    if (sitePage) {
      const product = sitePage.productId
        ? docsProducts.find(({ id }) => id === sitePage.productId)
        : undefined;

      if (sitePage.productId && !product) {
        throw new Error(
          `Unknown documentation product for ${sitePage.canonicalPath}.`
        );
      }

      return [
        {
          params: { slug: entry.id },
          props: {
            body: entry.body,
            canonicalPath: sitePage.canonicalPath,
            description: entry.data.description ?? "",
            product: product?.label,
            productId: product?.id,
            source: getDocsSourceUrl(entry.filePath, entry.id),
            title: entry.data.title,
          } satisfies MarkdownPageProps,
        },
      ];
    }

    const context = findDocsContext(withDocsBase(`/${entry.id}/`));

    if (!context) {
      return [];
    }

    return [
      {
        params: { slug: entry.id },
        props: {
          body: entry.body,
          canonicalPath: withDocsBase(`/${entry.id}/`),
          description: entry.data.description ?? "",
          docsVersion: context.version.label,
          docsVersionId: context.version.id,
          lifecycle: context.version.lifecycle,
          product: context.product.label,
          productId: context.product.id,
          source: getDocsSourceUrl(entry.filePath, entry.id),
          title: entry.data.title,
        } satisfies MarkdownPageProps,
      },
    ];
  });
}) satisfies GetStaticPaths;

export const GET: APIRoute = ({ props, request, site }) => {
  const markdownPage = props as MarkdownPageProps;
  const canonical = new URL(markdownPage.canonicalPath, site ?? request.url)
    .href;

  return new Response(createMarkdown(markdownPage, canonical), {
    headers: {
      "Content-Type": "text/markdown; charset=utf-8",
      "X-Content-Type-Options": "nosniff",
    },
  });
};
