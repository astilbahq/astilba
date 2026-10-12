// @ts-check
import { access } from "node:fs/promises";
import { dirname, resolve } from "node:path";

import { canonicalStrictDescendant } from "./filesystem-containment.mjs";

/** @param {string} root */
const hasWorkspace = async (root) => {
  try {
    await access(resolve(root, "pnpm-workspace.yaml"));
    return true;
  } catch (error) {
    if (
      !(error instanceof Error) ||
      !("code" in error) ||
      error.code !== "ENOENT"
    ) {
      throw error;
    }
    return false;
  }
};

/** @param {string} packageRoot @param {string} target */
export const assertToolContainment = async (packageRoot, target) => {
  let installationRoot = packageRoot;
  while (!(await hasWorkspace(installationRoot))) {
    const parent = dirname(installationRoot);
    if (parent === installationRoot) {
      installationRoot = packageRoot;
      break;
    }
    installationRoot = parent;
  }
  const modules = resolve(installationRoot, "node_modules");
  await canonicalStrictDescendant(installationRoot, modules);
  return await canonicalStrictDescendant(modules, target);
};
