import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { sessionOptions } from "./session";

export function useSessionRequest() {
  const queries = useQueryClient();
  const session = useQuery(sessionOptions).data;
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, [session?.user?.id, session?.mfaPending]);

  return async <T>(
    request: (signal: AbortSignal) => Promise<T>,
    apply: (value: T) => void,
  ) => {
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted) return;
    const before = queries.getQueryData(sessionOptions.queryKey);
    const isCurrent = () => {
      const current = queries.getQueryData(sessionOptions.queryKey);
      return (
        !controller.signal.aborted &&
        current?.user?.id === before?.user?.id &&
        current?.mfaPending === before?.mfaPending
      );
    };
    try {
      const value = await request(controller.signal);
      // A completed response can outlive both its screen and its signed-in account.
      if (isCurrent()) apply(value);
    } catch (error) {
      if (isCurrent()) throw error;
    }
  };
}
