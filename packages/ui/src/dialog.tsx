import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { forwardRef } from "react";

import { dialogStyles } from "./styles.js";

const classNames = (...values: (string | undefined)[]): string =>
  values.filter(Boolean).join(" ");

export const DialogRoot = BaseDialog.Root;
export type DialogRootProps<Payload = unknown> = BaseDialog.Root.Props<Payload>;

export const DialogTrigger = BaseDialog.Trigger;
export type DialogTriggerProps<Payload = unknown> =
  BaseDialog.Trigger.Props<Payload>;

export const DialogPortal = BaseDialog.Portal;
export type DialogPortalProps = BaseDialog.Portal.Props;

export const DialogClose = BaseDialog.Close;
export type DialogCloseProps = BaseDialog.Close.Props;

export const createDialogHandle = BaseDialog.createHandle;
export type DialogHandle<Payload = unknown> = BaseDialog.Handle<Payload>;

export interface DialogBackdropProps extends Omit<
  BaseDialog.Backdrop.Props,
  "className"
> {
  readonly className?: string;
}

export const DialogBackdrop = forwardRef<HTMLDivElement, DialogBackdropProps>(
  ({ className, ...properties }, ref) => (
    <BaseDialog.Backdrop
      {...properties}
      className={classNames(dialogStyles.backdrop, className)}
      ref={ref}
    />
  )
);

export interface DialogViewportProps extends Omit<
  BaseDialog.Viewport.Props,
  "className"
> {
  readonly className?: string;
}

export const DialogViewport = forwardRef<HTMLDivElement, DialogViewportProps>(
  ({ className, ...properties }, ref) => (
    <BaseDialog.Viewport
      {...properties}
      className={classNames(dialogStyles.viewport, className)}
      ref={ref}
    />
  )
);

export interface DialogPopupProps extends Omit<
  BaseDialog.Popup.Props,
  "className"
> {
  readonly className?: string;
}

export const DialogPopup = forwardRef<HTMLDivElement, DialogPopupProps>(
  ({ className, ...properties }, ref) => (
    <BaseDialog.Popup
      {...properties}
      className={classNames(dialogStyles.popup, className)}
      ref={ref}
    />
  )
);

export interface DialogTitleProps extends Omit<
  BaseDialog.Title.Props,
  "className"
> {
  readonly className?: string;
}

export const DialogTitle = forwardRef<HTMLHeadingElement, DialogTitleProps>(
  ({ className, ...properties }, ref) => (
    <BaseDialog.Title
      {...properties}
      className={classNames(dialogStyles.title, className)}
      ref={ref}
    />
  )
);

export interface DialogDescriptionProps extends Omit<
  BaseDialog.Description.Props,
  "className"
> {
  readonly className?: string;
}

export const DialogDescription = forwardRef<
  HTMLParagraphElement,
  DialogDescriptionProps
>(({ className, ...properties }, ref) => (
  <BaseDialog.Description
    {...properties}
    className={classNames(dialogStyles.description, className)}
    ref={ref}
  />
));

export const Dialog = {
  Backdrop: DialogBackdrop,
  Close: DialogClose,
  Description: DialogDescription,
  Popup: DialogPopup,
  Portal: DialogPortal,
  Root: DialogRoot,
  Title: DialogTitle,
  Trigger: DialogTrigger,
  Viewport: DialogViewport,
  createHandle: createDialogHandle,
} as const;
