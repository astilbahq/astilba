import {
  BookOpen,
  Check,
  ChevronDown,
  CircleAlert,
  Copy,
  Database,
  FileDigit,
  GitBranch,
  LockKeyhole,
  MoveRight,
  Rocket,
  ServerCog,
  ShieldCheck,
  Tags,
  type LucideIcon,
  type LucideProps,
} from "lucide-react";

import type { DocsIcon as DocsIconName } from "../docs/types";

type InterfaceIcon =
  | "approve-check"
  | "copy"
  | "down-caret"
  | "right-arrow"
  | "status-alert";

type IconName = DocsIconName | InterfaceIcon;

interface FillIconDefinition {
  kind: "fill";
  path: string;
}

interface StrokeIconDefinition {
  component: LucideIcon;
  kind: "stroke";
}

type IconDefinition = FillIconDefinition | StrokeIconDefinition;

const iconDefinitions: Record<IconName, IconDefinition> = {
  "approve-check": { component: Check, kind: "stroke" },
  "approve-check-circle": { component: ShieldCheck, kind: "stroke" },
  "cloudflare-workers": {
    kind: "fill",
    path: "m8.213.063 8.879 12.136-8.67 11.739h2.476l8.665-11.735-8.89-12.14Zm4.728 0 9.02 11.992-9.018 11.883h2.496L24 12.656v-1.199L15.434.063ZM7.178 2.02.01 11.398l-.01 1.2 7.203 9.644 1.238-1.676-6.396-8.556 6.361-8.313Z",
  },
  "code-branch": { component: GitBranch, kind: "stroke" },
  copy: { component: Copy, kind: "stroke" },
  database: { component: Database, kind: "stroke" },
  "down-caret": { component: ChevronDown, kind: "stroke" },
  "file-digit": { component: FileDigit, kind: "stroke" },
  link: { component: Tags, kind: "stroke" },
  "model-context-protocol": {
    kind: "fill",
    path: "M13.85 0a4.16 4.16 0 0 0-2.95 1.217L1.456 10.66a.835.835 0 0 0 0 1.18.835.835 0 0 0 1.18 0l9.442-9.442a2.49 2.49 0 0 1 3.541 0 2.49 2.49 0 0 1 0 3.541L8.59 12.97l-.1.1a.835.835 0 0 0 0 1.18.835.835 0 0 0 1.18 0l.1-.098 7.03-7.034a2.49 2.49 0 0 1 3.542 0l.049.05a2.49 2.49 0 0 1 0 3.54l-8.54 8.54a1.96 1.96 0 0 0 0 2.755l1.753 1.753a.835.835 0 0 0 1.18 0 .835.835 0 0 0 0-1.18l-1.753-1.753a.266.266 0 0 1 0-.394l8.54-8.54a4.185 4.185 0 0 0 0-5.9l-.05-.05a4.16 4.16 0 0 0-2.95-1.218c-.2 0-.401.02-.6.048a4.17 4.17 0 0 0-1.17-3.552A4.16 4.16 0 0 0 13.85 0m0 3.333a.84.84 0 0 0-.59.245L6.275 10.56a4.186 4.186 0 0 0 0 5.902 4.186 4.186 0 0 0 5.902 0L19.16 9.48a.835.835 0 0 0 0-1.18.835.835 0 0 0-1.18 0l-6.985 6.984a2.49 2.49 0 0 1-3.54 0 2.49 2.49 0 0 1 0-3.54l6.983-6.985a.835.835 0 0 0 0-1.18.84.84 0 0 0-.59-.245",
  },
  "open-book": { component: BookOpen, kind: "stroke" },
  padlock: { component: LockKeyhole, kind: "stroke" },
  "react-router": {
    kind: "fill",
    path: "M12.118 5.466a2.306 2.306 0 0 0-.623.08c-.278.067-.702.332-.953.583-.41.423-.49.609-.662 1.469-.08.423.41 1.43.847 1.734.45.317 1.085.502 2.065.608 1.429.16 1.84.636 1.84 2.197 0 1.377-.385 1.747-1.96 1.906-1.707.172-2.58.834-2.765 2.117-.106.781.41 1.76 1.125 2.091 1.627.768 3.15-.198 3.467-2.196.211-1.284.622-1.642 1.998-1.747 1.588-.133 2.409-.675 2.713-1.787.278-1.02-.304-2.157-1.297-2.554-.264-.106-.873-.238-1.35-.291-1.495-.16-1.879-.424-2.038-1.39-.225-1.337-.317-1.562-.794-2.09a2.174 2.174 0 0 0-1.613-.73Zm-4.785 4.36a2.145 2.145 0 0 0-.497.048c-1.469.318-2.17 2.051-1.35 3.295 1.178 1.774 3.944.953 3.97-1.177.012-1.193-.98-2.143-2.123-2.166ZM2.089 14.19a2.22 2.22 0 0 0-.427.052c-2.158.476-2.237 3.626-.106 4.182.53.145.582.145 1.111.013 1.191-.318 1.866-1.456 1.549-2.607-.278-1.02-1.144-1.664-2.127-1.64Zm19.824.008c-.233.002-.477.058-.784.162-1.39.477-1.866 2.092-.98 3.336.557.794 1.96 1.058 2.82.516 1.416-.874 1.363-3.057-.093-3.746-.38-.186-.663-.271-.963-.268Z",
  },
  "right-arrow": { component: MoveRight, kind: "stroke" },
  rocket: { component: Rocket, kind: "stroke" },
  server: { component: ServerCog, kind: "stroke" },
  "status-alert": { component: CircleAlert, kind: "stroke" },
};

interface DocsIconProps {
  className?: string;
  label?: string;
  name: IconName;
  size?: LucideProps["size"];
  strokeWidth?: LucideProps["strokeWidth"];
}

export const DocsIcon = ({
  className,
  label,
  name,
  size = 16,
  strokeWidth = 1.25,
}: DocsIconProps) => {
  const definition = iconDefinitions[name];

  if (definition.kind === "fill") {
    return (
      <svg
        aria-hidden={label ? undefined : true}
        aria-label={label}
        className={className}
        fill="currentColor"
        focusable="false"
        height={size}
        role={label ? "img" : undefined}
        viewBox="0 0 24 24"
        width={size}
        xmlns="http://www.w3.org/2000/svg"
      >
        <path d={definition.path} />
      </svg>
    );
  }

  const Icon = definition.component;

  return (
    <Icon
      absoluteStrokeWidth
      aria-hidden={label ? undefined : true}
      aria-label={label}
      className={className}
      focusable="false"
      role={label ? "img" : undefined}
      size={size}
      strokeWidth={strokeWidth}
    />
  );
};
