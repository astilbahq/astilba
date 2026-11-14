import { AutomationCredentials } from "../automation";
import { TerminalSessions } from "../troubleshooting/terminal-sessions";
import { Secrets } from "./environment-secrets";
import type { EnvProps } from "./environment.types";

export function EnvironmentSettings(props: EnvProps) {
  const path = {
    workspaceId: props.workspaceId,
    projectId: props.projectId,
    environmentId: props.environment.id,
  };
  const standard =
    !props.environment.purpose || props.environment.purpose === "standard";
  return (
    <>
      <Secrets {...props} />
      {props.write && standard && (
        <TerminalSessions path={path} owner={props.owner} />
      )}
      {props.owner && (
        <AutomationCredentials
          key={props.environment.id}
          path={path}
          readOnly={!standard}
        />
      )}
    </>
  );
}
