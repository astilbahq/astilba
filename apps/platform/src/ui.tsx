import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  type FormEvent,
  type ReactNode,
  useContext,
  useId,
  useState,
} from "react";
import { ApiError } from "./api/client";
import { sessionOptions } from "./api/session";
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
const FormErrorContext = createContext<{ id?: string; fields: string[] }>({
  fields: [],
});
export function Form({
  children,
  onSubmit,
  label,
  action,
}: {
  children: ReactNode;
  onSubmit: (data: FormData, form: HTMLFormElement) => void;
  label?: string;
  action: ReturnType<typeof useAction>;
}) {
  const id = useId();
  const error = action.formId === id ? action.error : null;
  const fields = error instanceof ApiError ? (error.detail.fields ?? []) : [];
  const errorId = error ? `${id}-error` : undefined;
  return (
    <FormErrorContext value={{ id: errorId, fields }}>
      <form
        className={s.stack}
        aria-label={label}
        aria-describedby={errorId}
        onSubmit={(e: FormEvent<HTMLFormElement>) => {
          e.preventDefault();
          action.setFormId(id);
          onSubmit(new FormData(e.currentTarget), e.currentTarget);
        }}
      >
        {errorId && (
          <span id={errorId} className={s.screenReaderOnly}>
            {error instanceof Error ? error.message : "Request failed."} Fields:{" "}
            {fields.join(", ")}
          </span>
        )}
        {children}
      </form>
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
}: {
  name: string;
  label: string;
  type?: string;
  required?: boolean;
  autoComplete?: string;
  minLength?: number;
}) {
  const error = useContext(FormErrorContext);
  const invalid = error.fields.includes(name);
  return (
    <label>
      {label}
      <input
        name={name}
        aria-invalid={invalid || undefined}
        aria-describedby={invalid ? error.id : undefined}
        type={type}
        required={required}
        autoComplete={autoComplete}
        minLength={minLength}
      />
    </label>
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
    <button type="submit" className={s.primary} disabled={busy}>
      {busy ? "Working…" : children}
    </button>
  );
}
