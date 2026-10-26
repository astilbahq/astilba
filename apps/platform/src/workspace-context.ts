import { createContext } from "react";
import type { Schema } from "./api/client";

export const WorkspaceContext = createContext<Schema["Workspace"] | undefined>(
  undefined,
);
