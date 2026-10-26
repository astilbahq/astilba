import { Field as BaseField } from "@base-ui/react/field";
import { Form as BaseForm } from "@base-ui/react/form";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  type FormEvent,
  type ReactNode,
  useContext,
  useId,
  useMemo,
  useState,
} from "react";
import { useTranslation } from "react-i18next";
import { field } from "../styled-system/recipes";
import { ApiError } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import type { FormValidation } from "./components/form-validation";
import { Input, type InputProps } from "./components/input";
import { Label } from "./components/label";
import { ErrorToast } from "./components/toast";
import * as s from "./styles";
export function ErrorNotice({
  error,
  message,
}: {
  error: unknown;
  message?: string;
}) {
  const { t } = useTranslation("common");
  if (!error) return null;
  return (
    <div className={s.error} role="alert">
      <p>
        {message ??
          (error instanceof Error
            ? error.message
            : t(($) => $.unexpectedError))}
      </p>
      {error instanceof ApiError && error.detail.fields && (
        <p>{t(($) => $.fields, { fields: error.detail.fields.join(", ") })}</p>
      )}
      {error instanceof ApiError && error.detail.requestId && (
        <small>{t(($) => $.request, { id: error.detail.requestId })}</small>
      )}
    </div>
  );
}
export function useAction() {
  const queries = useQueryClient();
  const [formId, setFormId] = useState<string>();
  const mutation = useMutation({
    mutationFn: async (fn: () => Promise<unknown>) => fn(),
    onSettled: () =>
      queries.invalidateQueries({ queryKey: sessionOptions.queryKey }),
  });
  return {
    ...mutation,
    formId,
    setFormId,
    run: (fn: () => Promise<unknown>) => mutation.mutate(fn),
  };
}
const FormErrorContext = createContext<{
  id?: string;
  fields: readonly string[];
  validation?: FormValidation;
  serverError?: unknown;
}>({
  fields: [],
});
export function Form({
  children,
  onSubmit,
  label,
  action,
  validation,
  errorMessage,
}: {
  children: ReactNode | ((error: string | null) => ReactNode);
  onSubmit: (data: FormData, form: HTMLFormElement) => void;
  label?: string;
  action: ReturnType<typeof useAction>;
  validation?: FormValidation;
  errorMessage?: string;
}) {
  const { t } = useTranslation("common");
  const id = useId();
  const error = action.formId === id ? action.error : null;
  const notifyError = error && validation?.shouldToastError?.(error);
  const inlineFields =
    error instanceof ApiError
      ? validation?.errorFields?.[error.detail.code]
      : undefined;
  const fields =
    inlineFields ??
    (error instanceof ApiError ? (error.detail.fields ?? []) : []);
  const errorId = error ? `${id}-error` : undefined;
  // Base UI clears individual errors on edit. Keep the last response stable
  // during a retry so its submit focus waits for the next server response.
  const [lastResponse, setLastResponse] = useState({ error, fields });
  if (error && error !== lastResponse.error) setLastResponse({ error, fields });
  const serverErrors = useMemo(
    () =>
      // Base UI owns clearing these markers. Field renders current-language text.
      Object.fromEntries(lastResponse.fields.map((name) => [name, "invalid"])),
    [lastResponse],
  );
  const formError =
    error && validation && !notifyError && !inlineFields?.length
      ? validation.message(error)
      : null;
  const content =
    typeof children === "function" ? (
      children(formError)
    ) : (
      <>
        {formError && (
          <p className={s.error} role="alert">
            {formError}
          </p>
        )}
        {children}
      </>
    );
  const formProps = {
    className: s.stack,
    "aria-label": label,
    onSubmit(e: FormEvent<HTMLFormElement>) {
      e.preventDefault();
      action.setFormId(id);
      onSubmit(new FormData(e.currentTarget), e.currentTarget);
    },
  };
  return (
    <FormErrorContext
      value={{
        id: errorId,
        fields: validation ? lastResponse.fields : fields,
        validation,
        serverError: lastResponse.error,
      }}
    >
      <ErrorToast
        error={notifyError ? error : null}
        message={validation?.message(error) ?? ""}
      />
      {validation ? (
        <BaseForm
          {...formProps}
          errors={serverErrors}
          validationMode="onSubmit"
        >
          {content}
        </BaseForm>
      ) : (
        <form {...formProps} aria-describedby={errorId}>
          {errorId && (
            <span id={errorId} className={s.screenReaderOnly}>
              {errorMessage ??
                (error instanceof Error
                  ? error.message
                  : t(($) => $.requestFailed))}{" "}
              {t(($) => $.fields, { fields: fields.join(", ") })}
            </span>
          )}
          {content}
        </form>
      )}
    </FormErrorContext>
  );
}
export function Field({
  name,
  label,
  type = "text",
  required = true,
  autoComplete,
  inputMode,
  autoCapitalize,
  spellCheck,
  minLength,
  min,
  max,
  defaultValue,
  labelAction,
  placeholder,
  size,
  inputComponent: InputComponent = Input,
}: {
  name: string;
  label: string;
  type?: string;
  required?: boolean;
  autoComplete?: string;
  inputMode?: InputProps["inputMode"];
  autoCapitalize?: InputProps["autoCapitalize"];
  spellCheck?: InputProps["spellCheck"];
  minLength?: number;
  min?: number;
  max?: number;
  defaultValue?: InputProps["defaultValue"];
  labelAction?: ReactNode;
  placeholder?: string;
  size?: InputProps["size"];
  inputComponent?: typeof Input;
}) {
  const id = useId();
  const error = useContext(FormErrorContext);
  const invalid = error.fields.includes(name);
  const styles = labelAction ? field({ hasAction: true }) : field();
  const validate = error.validation?.fields[name];
  const content = (
    <>
      {error.validation ? (
        <BaseField.Label
          render={<Label>{label}</Label>}
          htmlFor={id}
          className={styles.header}
        />
      ) : (
        <Label htmlFor={id} className={styles.header}>
          {label}
        </Label>
      )}
      <div className={styles.control}>
        <InputComponent
          id={id}
          size={size}
          name={name}
          {...(!error.validation
            ? {
                "aria-invalid": invalid || undefined,
                "aria-describedby": invalid ? error.id : undefined,
              }
            : {})}
          type={type}
          dir={
            type === "email" || autoComplete === "one-time-code"
              ? "ltr"
              : type === "password"
                ? undefined
                : "auto"
          }
          required={required}
          autoComplete={autoComplete}
          inputMode={inputMode}
          autoCapitalize={autoCapitalize}
          spellCheck={spellCheck}
          minLength={minLength}
          min={min}
          max={max}
          defaultValue={defaultValue}
          placeholder={placeholder}
        />
      </div>
      {labelAction && <div className={styles.action}>{labelAction}</div>}
      {error.validation && (
        <BaseField.Validity>
          {({ validity, value }) => {
            // Render from validity/value, not cached strings, so a language change
            // updates visible errors without revalidating or reviving cleared errors.
            const message =
              validity.valid === false
                ? (validate?.(value) ??
                  (error.serverError instanceof ApiError &&
                  error.fields.includes(name)
                    ? error.validation?.message(error.serverError, name)
                    : null))
                : null;
            return (
              <BaseField.Error className={styles.error}>
                {message}
              </BaseField.Error>
            );
          }}
        </BaseField.Validity>
      )}
    </>
  );
  return error.validation ? (
    <BaseField.Root name={name} validate={validate} className={styles.root}>
      {content}
    </BaseField.Root>
  ) : (
    <div className={styles.root}>{content}</div>
  );
}
export function text(data: FormData, name: string) {
  const value = data.get(name);
  return typeof value === "string" ? value : "";
}
export function BusyButton({
  busy,
  disabled = false,
  children,
}: {
  busy: boolean;
  disabled?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  return (
    <Button type="submit" variant="primary" disabled={busy || disabled}>
      {busy ? t(($) => $.working) : children}
    </Button>
  );
}
