// @ts-check
/// <reference types="node" />

import { mkdir, mkdtemp, realpath, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { relative, resolve, win32 } from "node:path";

import {
  canonicalStrictDescendant,
  strictDescendant,
} from "./filesystem-containment.mjs";

/**
 * @param {string} message
 * @returns {never}
 */
const fail = (message) => {
  throw new Error(message);
};

/**
 * @param {string} name
 * @param {() => unknown} operation
 */
const expectRejected = async (name, operation) => {
  try {
    await operation();
  } catch {
    return;
  }
  fail(`${name} was accepted unexpectedly.`);
};

/**
 * @param {string} name
 * @param {string} expectedRelative
 * @param {Promise<import("./filesystem-containment.mjs").Containment>} operation
 */
const expectAccepted = async (name, expectedRelative, operation) => {
  const containment = await operation;
  if (
    containment.relative !== expectedRelative ||
    containment.canonicalRoot === containment.canonicalTarget
  ) {
    fail(`${name} did not return a strict canonical descendant.`);
  }
};

const temporary = await mkdtemp(resolve(tmpdir(), "astilba-env-containment-"));
let darwinVarAlias = "not-applicable";
try {
  const root = resolve(temporary, "root");
  const child = resolve(root, "child");
  const sibling = resolve(temporary, "sibling");
  await Promise.all([
    mkdir(child, { recursive: true }),
    mkdir(sibling, { recursive: true }),
  ]);

  await expectAccepted(
    "Direct child",
    "child",
    canonicalStrictDescendant(root, child)
  );
  await expectRejected(
    "Equality",
    async () => await canonicalStrictDescendant(root, root)
  );
  await expectRejected(
    "Lexical parent escape",
    async () => await canonicalStrictDescendant(root, sibling)
  );

  const alias = resolve(temporary, "root-alias");
  await symlink(root, alias, process.platform === "win32" ? "junction" : "dir");
  await expectAccepted(
    "Physical alias with canonical child",
    "child",
    canonicalStrictDescendant(alias, child)
  );
  const redirectedChild = resolve(root, "redirected-child");
  await symlink(
    sibling,
    redirectedChild,
    process.platform === "win32" ? "junction" : "dir"
  );
  await expectRejected(
    "Physical in-root escape",
    async () => await canonicalStrictDescendant(root, redirectedChild)
  );

  if (process.platform === "darwin") {
    const canonicalVar = await realpath("/var").catch(() => undefined);
    const canonicalPrivateVar = await realpath("/private/var").catch(
      () => undefined
    );
    const observedVar =
      canonicalVar ?? fail("Darwin /var alias is not observable.");
    const observedPrivateVar =
      canonicalPrivateVar ?? fail("Darwin /var alias is not observable.");
    if (observedVar !== observedPrivateVar) {
      fail("Darwin /var alias resolved to surprising canonical paths.");
    }
    const nativeChild = "/private/var/tmp";
    const canonicalNativeChild = await realpath(nativeChild).catch(
      () => undefined
    );
    const observedNativeChild =
      canonicalNativeChild ??
      fail("Darwin /var alias has no observable strict child.");
    await expectAccepted(
      "Darwin /var alias",
      relative(observedVar, observedNativeChild),
      canonicalStrictDescendant("/var", nativeChild)
    );
    darwinVarAlias = "observed";
  }

  await expectRejected("Windows cross-drive path", () =>
    strictDescendant(win32, "C:\\astilba\\root", "D:\\astilba\\target")
  );
} finally {
  await rm(temporary, { force: true, recursive: true });
}

process.stdout.write(
  `${JSON.stringify({ darwinVarAlias, passed: true, windowsCrossDrive: true })}\n`
);
