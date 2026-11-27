import { type RefObject, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, useAction } from "../../ui";

/**
 * A button that asks before a storage account or key change management then makes at AWS. Each
 * of these needs a recent confirmation of identity, which opens over the dialog. A refusal stays
 * in the open dialog, which closes once the request is accepted and `run` has refreshed what it
 * changed. Most changes take the button away (`available`), so the dialog outlives it and hands
 * focus to `focusAfter`.
 */
export function ConfirmAction({
  label,
  title,
  description,
  confirm = label,
  danger = true,
  available,
  focusAfter,
  run,
}: {
  label: string;
  title: string;
  description: string;
  confirm?: string;
  danger?: boolean;
  available: boolean;
  focusAfter: RefObject<HTMLElement | null>;
  run: () => Promise<unknown>;
}) {
  const { t } = useTranslation("common");
  const { t: text } = useTranslation("backup-storage");
  const action = useAction({ recentAuth: true });
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  return (
    <>
      {available && (
        <div className={s.row}>
          <Button
            ref={trigger}
            type="button"
            variant={danger ? "danger" : "secondary"}
            onClick={() => {
              action.reset();
              setOpen(true);
            }}
          >
            {label}
          </Button>
        </div>
      )}
      <AlertDialogRoot
        open={open}
        onOpenChange={(next) => {
          if (!next && !action.isPending) setOpen(false);
        }}
      >
        <AlertDialogContent
          finalFocus={() => trigger.current ?? focusAfter.current ?? true}
        >
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
          <ErrorNotice error={action.error} />
          <AlertDialogActions>
            <AlertDialogClose
              render={
                <Button disabled={action.isPending}>
                  {text(($) => $.cancel)}
                </Button>
              }
            />
            <Button
              type="button"
              variant={danger ? "danger" : "primary"}
              // Keeps focus while busy, so a refusal is read beside it.
              focusableWhenDisabled
              disabled={action.isPending}
              onClick={(event) =>
                action.run(async () => {
                  await run();
                  setOpen(false);
                }, event.currentTarget)
              }
            >
              {action.isPending ? t(($) => $.working) : confirm}
            </Button>
          </AlertDialogActions>
        </AlertDialogContent>
      </AlertDialogRoot>
    </>
  );
}
