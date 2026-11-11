import { createContext, useContext } from "react";
import { ApiError } from "./client";

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

export function isRecentAuthRequired(error: unknown): error is ApiError {
  return (
    error instanceof ApiError && error.detail.code === "recent_auth_required"
  );
}
