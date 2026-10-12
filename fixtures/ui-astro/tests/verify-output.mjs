import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const outputDirectory = new URL("../dist/", import.meta.url);
const html = await readFile(new URL("index.html", outputDirectory), "utf-8");
const collapsibleHtml = await readFile(
  new URL("collapsible/index.html", outputDirectory),
  "utf-8"
);
const menuHtml = await readFile(
  new URL("menu/index.html", outputDirectory),
  "utf-8"
);
const scrollAreaHtml = await readFile(
  new URL("scroll-area/index.html", outputDirectory),
  "utf-8"
);
const hasClass = (element, className) =>
  element
    .match(/\bclass="(?<classes>[^"]*)"/u)
    ?.groups?.classes.split(/\s+/u)
    .includes(className) ?? false;

const continueButton = html.match(/<button[^>]*>Continue<\/button>/u)?.[0];
const docsLink = html.match(/<a[^>]*>Read the docs<\/a>/u)?.[0];
const iconButton = html.match(
  /<button[^>]*aria-label="More information"[^>]*>/u
)?.[0];
const projectLabel = html.match(/<label[^>]*>Project name<\/label>/u)?.[0];
const projectInput = html.match(
  /<input(?=[^>]*\bname="projectName")[^>]*>/u
)?.[0];
const projectDescription = html.match(
  /<p[^>]*>Used for the package name\.<\/p>/u
)?.[0];
const projectError = html.match(
  /<div[^>]*>Project names must be portable\.<\/div>/u
)?.[0];
const descriptionTextarea = html.match(
  /<textarea(?=[^>]*\bname="description")[^>]*>A useful project\.<\/textarea>/u
)?.[0];
const githubOwnerInput = html.match(
  /<input(?=[^>]*\bname="githubOwner")[^>]*>/u
)?.[0];
const githubOwnerError = html.match(
  /<div[^>]*>Use a valid GitHub account name\.<\/div>/u
)?.[0];

assert.ok(continueButton, "Expected a server-rendered Continue button");
assert.match(continueButton, /type="button"/u);
assert.ok(hasClass(continueButton, "astilba-control"));
assert.ok(docsLink, "Expected a server-rendered documentation link");
assert.match(docsLink, /href="\/docs"/u);
assert.ok(hasClass(docsLink, "astilba-control"));
assert.ok(iconButton, "Expected a server-rendered icon button");
assert.ok(hasClass(iconButton, "astilba-control"));
assert.ok(projectLabel, "Expected a server-rendered field label");
assert.ok(projectInput, "Expected a server-rendered text input");
assert.ok(projectDescription, "Expected a server-rendered field description");
assert.ok(projectError, "Expected a mounted server-rendered field error");
assert.ok(
  descriptionTextarea,
  "Expected a server-rendered description textarea"
);
assert.ok(githubOwnerInput, "Expected a server-rendered invalid input");
assert.ok(githubOwnerError, "Expected a server-rendered active field error");
assert.equal(
  projectLabel.match(/\bfor="(?<id>[^"]+)"/u)?.groups?.id,
  projectInput.match(/\bid="(?<id>[^"]+)"/u)?.groups?.id
);
assert.ok(hasClass(projectInput, "astilba-input"));
assert.equal(
  projectInput.match(/\baria-describedby="(?<ids>[^"]+)"/u)?.groups?.ids,
  projectDescription.match(/\bid="(?<id>[^"]+)"/u)?.groups?.id
);
assert.match(projectError, /\bhidden(?:=""|="hidden")?/u);
assert.doesNotMatch(projectInput, /project-name-error/u);
assert.ok(hasClass(descriptionTextarea, "astilba-input"));
assert.ok(hasClass(descriptionTextarea, "astilba-textarea"));
assert.match(githubOwnerInput, /\baria-invalid="true"/u);
assert.equal(
  githubOwnerInput.match(/\baria-describedby="(?<ids>[^"]+)"/u)?.groups?.ids,
  githubOwnerError.match(/\bid="(?<id>[^"]+)"/u)?.groups?.id
);
const stylesheetHref = html.match(
  /<link[^>]*rel="stylesheet"[^>]*href="(?<href>[^"]+)"/u
)?.groups?.href;

assert.ok(stylesheetHref, "Expected the Astro output to include a stylesheet");
const stylesheet = await readFile(
  new URL(stylesheetHref.replace(/^\//u, ""), outputDirectory),
  "utf-8"
);

assert.match(stylesheet, /--astilba-colors-canvas/u);
assert.match(stylesheet, /\.astilba-control\{/u);
assert.doesNotMatch(html, /<astro-island/u);
assert.doesNotMatch(html, /<script/u);

assert.match(
  menuHtml,
  /<astro-island\b(?=[^>]*\bclient="load")[^>]*>[\s\S]*?<button[^>]*>More actions<\/button>/u
);
assert.match(menuHtml, /<script/u);

assert.match(
  collapsibleHtml,
  /<astro-island\b(?=[^>]*\bclient="load")[^>]*>[\s\S]*?<button(?=[^>]*\btype="button")(?=[^>]*\baria-expanded="false")[^>]*>Deployment details<\/button>/u
);
assert.match(collapsibleHtml, /<script/u);

assert.match(
  scrollAreaHtml,
  /<astro-island\b(?=[^>]*\bclient="load")[^>]*>[\s\S]*?\bclass="[^"]*\bastilba-scroll-area-root\b[^"]*"/u
);
assert.match(
  scrollAreaHtml,
  /\bclass="[^"]*\bastilba-scroll-area-viewport\b[^"]*"[^>]*\bdata-fade="block"/u
);
assert.doesNotMatch(scrollAreaHtml, /data-href="base-ui-disable-scrollbar"/u);
assert.match(scrollAreaHtml, /<script/u);
