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
import { field } from "../styled-system/recipes";
import { ApiError } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import type { FormValidation } from "./components/form-validation";
import { Input, type InputProps } from "./components/input";
import { Label } from "./components/label";
import * as s from "./styles";
export function ErrorNotice({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <div className={s.error} role="alert">
      <p>
        {error instanceof Error
          ? error.message
          : "Something went wrong. Please retry."}
      </p>
      {error instanceof ApiError && error.detail.fields && (
        <p>Fields: {error.detail.fields.join(", ")}</p>
      )}
      {error instanceof ApiError && error.detail.requestId && (
        <small>Request {error.detail.requestId}</small>
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
  fields: string[];
  validation?: FormValidation;
}>({
  fields: [],
});
export function Form({
  children,
  onSubmit,
  label,
  action,
  validation,
}: {
  children: ReactNode;
  onSubmit: (data: FormData, form: HTMLFormElement) => void;
  label?: string;
  action: ReturnType<typeof useAction>;
  validation?: FormValidation;
}) {
  const id = useId();
  const error = action.formId === id ? action.error : null;
  const fields = error instanceof ApiError ? (error.detail.fields ?? []) : [];
  const errorId = error ? `${id}-error` : undefined;
  // Base UI clears individual errors on edit. Keep the last response stable
  // during a retry so its submit focus waits for the next server response.
  const [lastError, setLastError] = useState(error);
  if (error && error !== lastError) setLastError(error);
  const serverErrors = useMemo(
    () =>
      validation && lastError instanceof ApiError
        ? Object.fromEntries(
            (lastError.detail.fields ?? [])
              .filter((name) => Object.hasOwn(validation.fields, name))
              .map((name) => [name, validation.message(lastError, name)]),
          )
        : {},
    [lastError, validation],
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
    <FormErrorContext value={{ id: errorId, fields, validation }}>
      {validation ? (
        <BaseForm
          {...formProps}
          errors={serverErrors}
          validationMode="onSubmit"
        >
          {error && (
            <p className={s.error} role="alert">
              {validation.message(error)}
            </p>
          )}
          {children}
        </BaseForm>
      ) : (
        <form {...formProps} aria-describedby={errorId}>
          {errorId && (
            <span id={errorId} className={s.screenReaderOnly}>
              {error instanceof Error ? error.message : "Request failed."}{" "}
              Fields: {fields.join(", ")}
            </span>
          )}
          {children}
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
  minLength,
  labelAction,
  placeholder,
  variant,
  size,
  inputComponent: InputComponent = Input,
}: {
  name: string;
  label: string;
  type?: string;
  required?: boolean;
  autoComplete?: string;
  minLength?: number;
  labelAction?: ReactNode;
  placeholder?: string;
  variant?: InputProps["variant"];
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
          variant={variant}
          size={size}
          name={name}
          {...(!error.validation
            ? {
                "aria-invalid": invalid || undefined,
                "aria-describedby": invalid ? error.id : undefined,
              }
            : {})}
          type={type}
          required={required}
          autoComplete={autoComplete}
          minLength={minLength}
          placeholder={placeholder}
        />
      </div>
      {labelAction && <div className={styles.action}>{labelAction}</div>}
      {error.validation && (
        <BaseField.Validity>
          {({ validity, value }) => {
            // Native constraints run before validate; use the same schema message
            // for those failures instead of browser-language validation strings.
            const message =
              validity.valid === false && !validity.customError
                ? validate?.(value)
                : null;
            return (
              <BaseField.Error
                className={styles.error}
                {...(message ? { children: message } : {})}
              />
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
  children,
}: {
  busy: boolean;
  children: ReactNode;
}) {
  return (
    <Button type="submit" variant="primary" disabled={busy}>
      {busy ? "Working…" : children}
    </Button>
  );
}
