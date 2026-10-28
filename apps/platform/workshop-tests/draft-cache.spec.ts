import { expect, test } from "@playwright/test";
import { QueryClient } from "@tanstack/react-query";
import { draftOptions, publishDraft } from "../src/api/environment";
import { revision } from "../src/workshop/environment-fixtures";

for (const phase of ["pending", "fulfilled"] as const) {
  test(`a ${phase} older draft read cannot replace a saved revision`, async () => {
    const queries = new QueryClient({
      defaultOptions: { queries: { gcTime: Infinity } },
    });
    const path = {
      workspaceId: "workspace",
      projectId: "project",
      environmentId: "environment",
    };
    const { queryKey } = draftOptions(path);
    const saved = { ...revision, id: "saved-revision" };
    queries.setQueryData(queryKey, revision);
    let finish!: (value: typeof revision) => void;
    const read = queries.fetchQuery({
      queryKey,
      queryFn: () =>
        new Promise<typeof revision>((resolve) => {
          finish = resolve;
        }),
    });
    try {
      if (phase === "fulfilled") {
        // Resolve the retryer before publication, while its cache commit is queued.
        finish(revision);
        await new Promise<void>((resolve, reject) =>
          queueMicrotask(() => {
            publishDraft(queries, path, saved).then(resolve, reject);
          }),
        );
      } else {
        const publication = publishDraft(queries, path, saved);
        finish(revision);
        await publication;
      }
      await Promise.allSettled([read]);
      expect(queries.getQueryData(queryKey)).toEqual(saved);
    } finally {
      queries.clear();
    }
  });
}
