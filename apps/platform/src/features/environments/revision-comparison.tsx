import type { ReactNode, Ref } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { Label } from "../../components/label";
import { Textarea } from "../../components/textarea";
import * as e from "./environment.styles";

export function RevisionComparison({
  id,
  label,
  title,
  description,
  draft,
  revision,
  revisionLabel,
  headingRef,
  headerAction,
  children,
}: {
  id: string;
  label?: string;
  title: string;
  description: string;
  draft: string;
  revision: Schema["Revision"];
  revisionLabel: string;
  headingRef: Ref<HTMLHeadingElement>;
  headerAction?: ReactNode;
  children?: ReactNode;
}) {
  const { t } = useTranslation("environment");
  return (
    <section
      className={e.comparison}
      aria-label={label}
      aria-labelledby={label ? undefined : id}
    >
      <div className={e.configurationHeader}>
        <h3
          id={id}
          ref={headingRef}
          tabIndex={-1}
          className={e.comparisonHeading}
        >
          {title}
        </h3>
        {headerAction}
      </div>
      <p className={e.muted}>{description}</p>
      <div className={e.comparisonColumns}>
        <div className={e.intro}>
          <Label htmlFor={`${id}-draft`}>
            {t(($) => $.configurationEditor.workingDraft)}
          </Label>
          <Textarea
            id={`${id}-draft`}
            readOnly
            spellCheck={false}
            value={draft}
            className={e.comparisonCode}
          />
        </div>
        <div className={e.intro}>
          <Label htmlFor={`${id}-revision`}>{revisionLabel}</Label>
          <Textarea
            id={`${id}-revision`}
            readOnly
            spellCheck={false}
            value={JSON.stringify(revision.specification, null, 2)}
            className={e.comparisonCode}
          />
        </div>
      </div>
      {children}
    </section>
  );
}
