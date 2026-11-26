import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { createRoot } from "react-dom/client";
import { createQueryClient } from "./api/query-client";
import { initializeI18n } from "./i18n/instance";
import { LocaleProvider } from "./i18n/provider";
import { router } from "./router";
import "./global.css";
export const queryClient = createQueryClient({ staleTime: 15000 });
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
