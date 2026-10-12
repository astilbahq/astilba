// @ts-check
/// <reference types="node" />

import { realpath } from "node:fs/promises";
import { isAbsolute, relative as relativePath, sep } from "node:path";

/**
 * @typedef {{
 *   isAbsolute(path: string): boolean,
 *   relative(from: string, to: string): string,
 *   sep: string
 * }} PathApi
 */

/**
 * @typedef {{
 *   canonicalRoot: string,
 *   canonicalTarget: string,
 *   relative: string
 * }} Containment
 */

/**
 * Derives a strict descendant path from already-canonical filesystem paths.
 *
 * @param {PathApi} pathApi
 * @param {string} canonicalRoot
 * @param {string} canonicalTarget
 * @returns {Containment}
 */
export const strictDescendant = (pathApi, canonicalRoot, canonicalTarget) => {
  const relative = pathApi.relative(canonicalRoot, canonicalTarget);
  if (
    relative === "" ||
    relative === ".." ||
    relative.startsWith(`..${pathApi.sep}`) ||
    pathApi.isAbsolute(relative)
  ) {
    throw new Error(
      "Filesystem target is not a strict descendant of its root."
    );
  }
  return Object.freeze({ canonicalRoot, canonicalTarget, relative });
};

const nativePath = { isAbsolute, relative: relativePath, sep };

/**
 * Canonicalises existing filesystem paths before deriving a strict descendant.
 *
 * @param {string} root
 * @param {string} target
 * @param {PathApi} [pathApi]
 * @returns {Promise<Containment>}
 */
export const canonicalStrictDescendant = async (
  root,
  target,
  pathApi = nativePath
) => strictDescendant(pathApi, await realpath(root), await realpath(target));
