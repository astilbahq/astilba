import { Toast as BaseToast } from "@base-ui/react/toast";
import {
  type CSSProperties,
  type ReactNode,
  useEffect,
  useId,
  useRef,
} from "react";
import { useTranslation } from "react-i18next";
import { toast as toastRecipe } from "../../styled-system/recipes";
import { Button } from "./button";
import { IconButton } from "./icon-button";
import { CheckIcon, Icon } from "./icons";

export const useToast = BaseToast.useToastManager;

export function Toast({ toast }: { toast: BaseToast.Root.ToastObject }) {
  const { t } = useTranslation("common");
  const { close } = useToast();
  useEffect(() => {
    if (toast.limited) close(toast.id);
  }, [toast.limited, toast.id, close]);
  const styles = toastRecipe();
  const fallbackTitle =
    toast.type === "error"
      ? t(($) => $.toast.error)
      : toast.type === "success"
        ? t(($) => $.toast.success)
        : t(($) => $.toast.notification);
  return (
    <BaseToast.Root toast={toast} className={styles.root} swipeDirection={[]}>
      <BaseToast.Content
        className={styles.content}
        render={(props, state) => (
          <div
            {...props}
            inert={state.behind && !state.expanded}
            aria-hidden={(state.behind && !state.expanded) || undefined}
          />
        )}
      >
        <span className={styles.indicator}>
          {toast.type === "success" ? (
            <CheckIcon />
          ) : (
            <Icon>
              <circle cx="12" cy="12" r="9" />
              <path d="M12 8v4m0 4h.01" />
            </Icon>
          )}
        </span>
        <div className={styles.text}>
          <BaseToast.Title className={styles.title}>
            {toast.title ?? fallbackTitle}
          </BaseToast.Title>
          <BaseToast.Description className={styles.description} />
          {toast.actionProps && (
            <BaseToast.Action
              render={
                <Button variant="secondary" size="sm">
                  {toast.actionProps.children}
                </Button>
              }
              className={styles.action}
            />
          )}
        </div>
        <BaseToast.Close
          aria-hidden={false}
          render={
            <IconButton size="sm" aria-label={t(($) => $.toast.dismiss)}>
              <Icon>
                <path d="m6 6 12 12M6 18 18 6" />
              </Icon>
            </IconButton>
          }
          className={styles.close}
        />
      </BaseToast.Content>
    </BaseToast.Root>
  );
}

function ToastViewport() {
  const { t } = useTranslation("common");
  const { toasts } = useToast();
  const styles = toastRecipe();
  // Keep exiting cards in the measured area until their fade completes.
  const layoutToasts = toasts.filter((toast) => !toast.limited);
  const stackStyle = {
    "--toast-count": layoutToasts.length,
    "--toast-total-height": `${layoutToasts.reduce((height, toast) => height + (toast.height ?? 0), 0)}px`,
  } as CSSProperties;
  return (
    <BaseToast.Portal>
      <BaseToast.Viewport
        className={styles.viewport}
        aria-label={t(($) => $.toast.region)}
        data-empty={toasts.length === 0 ? "" : undefined}
        data-stacked={layoutToasts.length > 1 ? "" : undefined}
      >
        <div className={styles.stack} style={stackStyle}>
          {toasts.map((toast) => (
            <Toast key={toast.id} toast={toast} />
          ))}
        </div>
      </BaseToast.Viewport>
    </BaseToast.Portal>
  );
}

export function ToastProvider({ children }: { children: ReactNode }) {
  return (
    <BaseToast.Provider limit={3}>
      {children}
      <ToastViewport />
    </BaseToast.Provider>
  );
}

// One notification per error source. Locale changes update an existing toast;
// dismissing it keeps it dismissed until a new failure arrives.
export function ErrorToast({
  error,
  message,
}: {
  error: unknown;
  message: string;
}) {
  const { add, close, update } = useToast();
  const id = useId();
  const previousError = useRef<unknown>(null);
  useEffect(() => {
    if (!error) {
      previousError.current = null;
      close(id);
    } else if (error !== previousError.current) {
      previousError.current = error;
      add({ id, type: "error", description: message, timeout: 0 });
    } else {
      update(id, { description: message });
    }
  }, [error, message, add, close, update, id]);
  useEffect(
    () => () => {
      previousError.current = null;
      close(id);
    },
    [close, id],
  );
  return null;
}
