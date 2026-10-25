import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { createRoot } from "react-dom/client";
import { initializeI18n } from "./i18n/instance";
import { LocaleProvider } from "./i18n/provider";
import { router } from "./router";
import "./global.css";
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, staleTime: 15000 },
    mutations: { retry: false },
  },
});
const root = document.getElementById("root");
if (!root) throw new Error("Root missing");
await initializeI18n();
createRoot(root).render(
  <LocaleProvider>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </LocaleProvider>,
);
