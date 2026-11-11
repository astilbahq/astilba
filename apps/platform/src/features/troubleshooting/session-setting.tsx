import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { Button } from "../../components/button";
import { Checkbox } from "../../components/checkbox";
import { Label } from "../../components/label";
import * as s from "../../styles";
import { BusyButton, Form, useAction } from "../../ui";
import { policyOptions, policyPath, sessionsKey } from "./session-queries";
import { SessionErrorNotice, SessionLoadFailed } from "./session-text";
import * as styles from "./troubleshooting.styles";

const hasCode = (error: unknown, code: string) =>
  error instanceof ApiError && error.detail.code === code;

export function SessionSetting({ path }: { path: EnvironmentPath }) {
  const { t } = useTranslation("troubleshooting");
  const queries = useQueryClient();
  const action = useAction();
  const options = policyOptions(path);
  const policy = useQuery(options);
  // Holds the generation it was edited from, so a newer saved setting is noticed
  // before the owner overwrites it.
  const [draft, setDraft] = useState<Schema["SaveTroubleshootingPolicy"]>();
  const [saved, setSaved] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const loadCurrent = useRef<HTMLButtonElement>(null);
  const ownersOnlyHelp = useId();
  const conflictText = useId();
  const save = (body: Schema["SaveTroubleshootingPolicy"]) =>
    action.run(async () => {
      try {
        const next = await result(
          client.PUT(policyPath, { params: { path }, body }),
        );
        queries.setQueryData(options.queryKey, next);
        setDraft(undefined);
        setSaved(true);
      } finally {
        await queries.invalidateQueries({ queryKey: options.queryKey });
        await queries.invalidateQueries({
          queryKey: sessionsKey(path.workspaceId),
        });
      }
    });
  const current = draft ?? policy.data;
  const edit = (change: Partial<Schema["SaveTroubleshootingPolicy"]>) => {
    if (!policy.data) return;
    action.reset();
    setSaved(false);
    setDraft({
      enabled: policy.data.enabled,
      ownersOnly: policy.data.ownersOnly,
      generation: policy.data.generation,
      ...draft,
      ...change,
    });
  };
  const stale = !!draft && draft.generation !== policy.data?.generation;
  const changed =
    !!draft &&
    (draft.enabled !== policy.data?.enabled ||
      draft.ownersOnly !== policy.data?.ownersOnly);
  const conflict = stale && hasCode(action.error, "conflict");
  useEffect(() => {
    // Save is disabled now, and the only way on comes before it in the form.
    if (conflict) loadCurrent.current?.focus();
  }, [conflict]);
  return (
    <div className={styles.part}>
      <h3 ref={heading} tabIndex={-1} className={styles.heading}>
        {t(($) => $.setting.title)}
      </h3>
      {policy.data && !policy.data.available && (
        <p className={s.notice}>{t(($) => $.setting.unavailable)}</p>
      )}
      {policy.isPending ? (
        <p role="status">{t(($) => $.setting.loading)}</p>
      ) : !current ? (
        <SessionLoadFailed
          message={t(($) => $.setting.loadFailed)}
          heading={heading}
          retry={() => void policy.refetch()}
        />
      ) : (
        <Form
          action={action}
          label={t(($) => $.setting.title)}
          onSubmit={() => {
            if (draft) save(draft);
          }}
        >
          {!conflict && <SessionErrorNotice error={action.error} />}
          {saved && <p role="status">{t(($) => $.setting.saved)}</p>}
          <Label className={styles.choice}>
            <Checkbox
              name="enabled"
              checked={current.enabled}
              disabled={action.isPending}
              onChange={(event) =>
                edit({ enabled: event.currentTarget.checked })
              }
            />
            {t(($) => $.setting.enabled)}
          </Label>
          <div className={styles.intro}>
            <Label className={styles.choice}>
              <Checkbox
                name="ownersOnly"
                checked={current.ownersOnly}
                aria-describedby={ownersOnlyHelp}
                disabled={action.isPending || !current.enabled}
                onChange={(event) =>
                  edit({ ownersOnly: event.currentTarget.checked })
                }
              />
              {t(($) => $.setting.ownersOnly)}
            </Label>
            <p id={ownersOnlyHelp} className={styles.help}>
              {t(($) => $.setting.ownersOnlyHelp)}
            </p>
          </div>
          <div className={styles.intro}>
            <p className={styles.help}>{t(($) => $.setting.widenHelp)}</p>
            <p className={styles.help}>{t(($) => $.setting.narrowingHelp)}</p>
          </div>
          {stale && (
            <div className={styles.part} role="alert">
              <p id={conflictText}>{t(($) => $.setting.conflict)}</p>
              <div>
                <Button
                  ref={loadCurrent}
                  type="button"
                  aria-describedby={conflictText}
                  disabled={action.isPending}
                  onClick={() => {
                    action.reset();
                    setDraft(undefined);
                    heading.current?.focus();
                  }}
                >
                  {t(($) => $.setting.loadCurrent)}
                </Button>
              </div>
            </div>
          )}
          <div>
            <BusyButton busy={action.isPending} disabled={!changed || stale}>
              {t(($) => $.setting.save)}
            </BusyButton>
          </div>
        </Form>
      )}
    </div>
  );
}
