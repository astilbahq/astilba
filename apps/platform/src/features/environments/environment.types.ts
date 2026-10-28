import type { Schema } from "../../api/client";
export type EnvProps = {
  workspaceId: string;
  projectId: string;
  environment: Schema["Environment"];
  write: boolean;
  owner: boolean;
};

export type EnvironmentSection =
  | "overview"
  | "configuration"
  | "deployments"
  | "settings";
