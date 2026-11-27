import {
  notifyManager,
  QueryClient,
  type DefaultOptions,
} from "@tanstack/react-query";

// The query library tells components about a change in a zero-delay timer. The browser can run a
// response or a closing dialog first, so a component can miss a state it should have shown, such
// as a retry in progress, or move focus to a control the change is about to remove. A microtask
// runs before any of those.
notifyManager.setScheduler(queueMicrotask);

export function createQueryClient(queries: DefaultOptions["queries"]) {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, ...queries },
      mutations: { retry: false },
    },
  });
}
