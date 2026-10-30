import { expect, test } from "@playwright/test";
import { QueryClient } from "@tanstack/react-query";
import {
  draftOptions,
  layoutOptions,
  publishDraft,
  publishLayout,
} from "../src/api/environment";
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
            publishDraft(queries, path, saved, revision.id).then(
              resolve,
              reject,
            );
          }),
        );
      } else {
        const publication = publishDraft(queries, path, saved, revision.id);
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

for (const kind of ["draft", "layout", "restored-layout"] as const) {
  test(`a delayed ${kind} write cannot replace a newer observed save`, async () => {
    const queries = new QueryClient({
      defaultOptions: { queries: { gcTime: Infinity } },
    });
    const path = {
      workspaceId: "workspace",
      projectId: "project",
      environmentId: "environment",
    };
    try {
      if (kind === "draft") {
        const { queryKey } = draftOptions(path);
        const older = { ...revision, id: "older-save" };
        const newer = { ...revision, id: "newer-save" };
        await queries.fetchQuery({ queryKey, queryFn: async () => newer });
        await publishDraft(queries, path, older, revision.id);
        expect(queries.getQueryData(queryKey)).toEqual(newer);
      } else {
        const { queryKey } = layoutOptions(path);
        queries.setQueryData(queryKey, {
          positions: { api: { x: 30, y: 40 } },
        });
        const previous = queries.getQueryState(queryKey);
        const older = { positions: { api: { x: 10, y: 20 } } };
        const newer = {
          positions: { api: { x: kind === "layout" ? 50 : 30, y: 40 } },
        };
        await queries.fetchQuery({ queryKey, queryFn: async () => newer });
        await publishLayout(queries, path, older, previous);
        expect(queries.getQueryData(queryKey)).toEqual(newer);
      }
    } finally {
      queries.clear();
    }
  });
}
