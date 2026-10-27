import { AlertDialog as BaseAlertDialog } from "@base-ui/react/alert-dialog";
import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { alertDialog } from "../../styled-system/recipes";

export const AlertDialogRoot = BaseAlertDialog.Root;
export const AlertDialogTrigger = BaseAlertDialog.Trigger;
export const AlertDialogClose = BaseAlertDialog.Close;

export function AlertDialogContent({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseAlertDialog.Popup>, "className"> & {
  className?: string;
}) {
  const styles = alertDialog();
  return (
    <BaseAlertDialog.Portal>
      <BaseAlertDialog.Backdrop className={styles.backdrop} />
      <BaseAlertDialog.Viewport className={styles.viewport}>
        <BaseAlertDialog.Popup
          {...props}
          className={cx(styles.content, className)}
        />
      </BaseAlertDialog.Viewport>
    </BaseAlertDialog.Portal>
  );
}
export function AlertDialogTitle({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseAlertDialog.Title>, "className"> & {
  className?: string;
}) {
  return (
    <BaseAlertDialog.Title
      {...props}
      className={cx(alertDialog().title, className)}
    />
  );
}
export function AlertDialogDescription({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseAlertDialog.Description>, "className"> & {
  className?: string;
}) {
  return (
    <BaseAlertDialog.Description
      {...props}
      className={cx(alertDialog().description, className)}
    />
  );
}
export function AlertDialogActions({
  className,
  ...props
}: ComponentProps<"div">) {
  return <div {...props} className={cx(alertDialog().actions, className)} />;
}
