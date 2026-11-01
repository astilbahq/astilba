import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../components/icons";
import * as v from "./visual-editor.styles";

type ResourceKind = "web" | "worker" | "postgres" | "network" | "volume";

export function ResourceIcon({ kind }: { kind: ResourceKind }) {
  return (
    <Icon>
      {kind === "postgres" ? (
        <>
          <ellipse cx="12" cy="5" rx="8" ry="3" />
          <path d="M4 5v14c0 4 16 4 16 0V5M4 12c0 4 16 4 16 0" />
        </>
      ) : kind === "volume" ? (
        <>
          <rect x="3" y="6" width="18" height="12" rx="2" />
          <path d="M3 13h18M7 16h.01M11 16h.01" />
        </>
      ) : kind === "network" ? (
        <>
          <rect x="9" y="3" width="6" height="5" rx="1" />
          <path d="M12 8v4M5 16v-4h14v4" />
          <rect x="2" y="16" width="6" height="5" rx="1" />
          <rect x="16" y="16" width="6" height="5" rx="1" />
        </>
      ) : kind === "worker" ? (
        <>
          <path d="m13 3-9 11h7l-1 7 10-12h-7z" />
        </>
      ) : (
        <>
          <rect x="3" y="4" width="18" height="16" rx="2" />
          <path d="M3 9h18M7 6.5h.01M10 6.5h.01m-3 7 2 2-2 2m5 0h4" />
        </>
      )}
    </Icon>
  );
}

export function ResourceSummary({
  name,
  kind,
  detail,
  instances = 1,
}: {
  name: ReactNode;
  kind: ResourceKind;
  detail?: string;
  instances?: number;
}) {
  const { t } = useTranslation("environment");
  return (
    <div className={v.resource}>
      <span className={v.resourceIcon}>
        <ResourceIcon kind={kind} />
      </span>
      <div className={v.resourceText}>
        <span className={v.resourceName}>{name}</span>
        <span className={v.resourceKind}>
          {t(($) => $.visualEditor.kinds[kind])}
          {instances > 1 && (
            <> · {t(($) => $.visualEditor.instances, { count: instances })}</>
          )}
        </span>
        {detail && (
          <span className={v.resourceDetail} title={detail} dir="ltr">
            {detail}
          </span>
        )}
      </div>
    </div>
  );
}
