import { createContext, useContext } from "react";
import { ApiError, type Schema } from "./client";

/**
 * Asks the person to confirm their identity, renewing the session's recent authentication.
 * Resolves true once it is renewed, or false if they decline. Concurrent callers share one
 * prompt and its outcome. `origin` is the control that started the action, which gets focus
 * back when the prompt closes; it defaults to the focused element. `signal` withdraws the
 * request when the caller goes away, resolving it false and closing the prompt once nobody
 * else is waiting, so a screen the person has left never acts on their confirmation.
 */
export type StepUp = (options?: {
  origin?: Element | null;
  signal?: AbortSignal;
}) => Promise<boolean>;

// Null outside a StepUpProvider, where a refusal is shown as it is.
export const StepUpContext = createContext<StepUp | null>(null);

export function useStepUp() {
  return useContext(StepUpContext);
}

// For a control whose only purpose is confirming identity, which needs a provider above it.
export function useRequiredStepUp() {
  const stepUp = useContext(StepUpContext);
  if (!stepUp) throw new Error("Identity confirmation needs a StepUpProvider");
  return stepUp;
}

/**
 * Asks for the account's current credential, the one a step-up asks for, and submits a change
 * that needs it, such as a new password, from the same dialog. Resolves with the change's
 * result, or undefined if the person declines or `signal` withdraws the request. A wrong
 * credential, or too many attempts, stays in the dialog to try again; any other failure closes
 * it and rejects, for the screen that asked to show.
 */
export type ConfirmCredential = <T>(
  submit: (current: Schema["Credential"], signal: AbortSignal) => Promise<T>,
  options?: { origin?: Element | null; signal?: AbortSignal },
) => Promise<T | undefined>;

export const ConfirmCredentialContext = createContext<ConfirmCredential | null>(
  null,
);

// For a change to the account's credentials, which needs a provider above it.
export function useConfirmCredential() {
  const confirm = useContext(ConfirmCredentialContext);
  if (!confirm) throw new Error("Changing credentials needs a StepUpProvider");
  return confirm;
}

export function isRecentAuthRequired(error: unknown): error is ApiError {
  return (
    error instanceof ApiError && error.detail.code === "recent_auth_required"
  );
}
