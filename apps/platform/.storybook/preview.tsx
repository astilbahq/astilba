import type { Preview } from "@storybook/tanstack-react";
import { passthrough } from "msw";
import { mswLoader } from "msw-storybook-addon/csf3";
import { setupWorker } from "msw/browser";
import { http } from "msw/http";
import { useGlobals } from "storybook/preview-api";
import { configure } from "storybook/test";
import * as auth from "../src/features/auth/auth.styles";
import { guest, sessionHandlers } from "../src/workshop/fixtures";
import {
  createWorkshopState,
  WorkshopProviders,
} from "../src/workshop/providers";
import "../src/global.css";

// Play starts at a story's first commit, so its waits also span the page's own loading and,
// on a busy machine, most of a second for each mocked response. The default of 1000 ms fails
// there while the page is still working correctly.
configure({ asyncUtilTimeout: 5000 });

const preview: Preview = {
  initialGlobals: { theme: "light", locale: "en" },
  globalTypes: {
    theme: {
      description: "Canvas theme",
      toolbar: {
        icon: "circlehollow",
        items: ["light", "dark", "system"],
        dynamicTitle: true,
      },
    },
    locale: {
      description: "Canvas language",
      toolbar: {
        icon: "globe",
        items: [
          { value: "auto", title: "Automatic" },
          { value: "en", title: "English" },
          { value: "en-XA", title: "Pseudo (expanded)" },
          { value: "en-XB", title: "Pseudo (RTL)" },
        ],
        dynamicTitle: true,
      },
    },
  },
  parameters: {
    layout: "fullscreen",
    a11y: { test: "error" },
    msw: { handlers: { session: sessionHandlers() } },
  },
  loaders: [
    mswLoader(async () => {
      const worker = setupWorker(
        http.all("*", ({ request }) => {
          if (new URL(request.url).pathname.startsWith("/api/")) {
            throw new Error(
              `Missing workshop API fixture: ${request.method} ${request.url}`,
            );
          }
          return passthrough();
        }),
      );
      await worker.start({
        quiet: true,
        onUnhandledFrame: "bypass",
      });
      return worker;
    }),
    async ({ parameters }) => ({
      workshop: await createWorkshopState(parameters.workshopSession ?? guest),
    }),
  ],
  decorators: [
    function WorkshopDecorator(Story, context) {
      const [, updateGlobals] = useGlobals();
      return (
        <WorkshopProviders
          key={context.id}
          state={context.loaded.workshop}
          theme={context.globals.theme}
          locale={context.globals.locale}
          onThemeChange={(theme) => updateGlobals({ theme })}
          onLocaleChange={(locale) => updateGlobals({ locale })}
        >
          <div className={auth.palette}>
            <Story />
          </div>
        </WorkshopProviders>
      );
    },
  ],
};

export default preview;
