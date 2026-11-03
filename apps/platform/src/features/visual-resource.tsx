import type { ReactNode, Ref } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../api/client";
import { Button } from "../components/button";
import { IconButton } from "../components/icon-button";
import { Icon } from "../components/icons";
import * as s from "../styles";
import * as v from "./visual-editor.styles";

type ResourceKind = "web" | "worker" | "postgres" | "network" | "volume";

export function InspectorHeader({
  name,
  kind,
  onClose,
  ref,
}: {
  name: string;
  kind: ResourceKind;
  onClose: () => void;
  ref: Ref<HTMLHeadingElement>;
}) {
  const { t } = useTranslation("environment");
  return (
    <div className={v.inspectorHeader}>
      <div>
        <h3 className={v.inspectorTitle} ref={ref} tabIndex={-1}>
          {name}
        </h3>
        <p className={s.muted}>{t(($) => $.visualEditor.kinds[kind])}</p>
      </div>
      <IconButton
        aria-label={t(($) => $.visualEditor.closeProperties)}
        onClick={onClose}
      >
        <Icon>
          <path d="m6 6 12 12M18 6 6 18" />
        </Icon>
      </IconButton>
    </div>
  );
}

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

export function NetworkProperties({
  name,
  onClose,
  ref,
}: {
  name: string;
  onClose: () => void;
  ref: Ref<HTMLHeadingElement>;
}) {
  const { t } = useTranslation("environment");
  return (
    <section className={v.inspector} aria-label={`Properties for ${name}`}>
      <InspectorHeader ref={ref} name={name} kind="network" onClose={onClose} />
      <p className={s.muted}>{t(($) => $.visualEditor.networkDescription)}</p>
    </section>
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

export function ResourceList({
  spec,
  selected,
  onOpen,
}: {
  spec: Schema["Specification"];
  selected?: string;
  onOpen: (id: string, from: HTMLElement) => void;
}) {
  const choice = (id: string, name: string) => (
    <Button
      type="button"
      variant="ghost"
      className={v.resourceChoice}
      aria-pressed={selected === id}
      onClick={(event) => onOpen(id, event.currentTarget)}
    >
      {name}
    </Button>
  );
  return (
    <ul className={v.resourceList}>
      {(spec.networks ?? []).map((network) => (
        <li key={network.id}>
          <ResourceSummary name={network.name} kind="network" />
        </li>
      ))}
      {spec.applications.map((a) => (
        <li key={a.id}>
          <ResourceSummary
            name={choice(a.id, a.name)}
            kind={a.kind ?? "web"}
            instances={a.replicas}
            detail={
              a.source.kind === "git" ? a.source.repository : a.source.image
            }
          />
        </li>
      ))}
      {spec.volumes?.map((volume) => (
        <li key={volume.id}>
          <ResourceSummary
            name={choice(volume.id, volume.name)}
            kind="volume"
          />
        </li>
      ))}
    </ul>
  );
}
