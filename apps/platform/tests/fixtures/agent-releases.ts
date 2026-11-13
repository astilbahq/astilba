import { expect, type APIRequestContext } from "@playwright/test";

/** An agent built from this code acknowledges the secret releases it receives. */
export const acknowledgesReleases = { acknowledgesReleases: true } as const;

type Released = {
  id: string;
  secretRelease?: { epoch: number; digest: string; values: boolean };
};

/**
 * Acknowledges the values a polled command brought, as an agent does once it has journaled
 * them. Management otherwise releases them again after their lease.
 */
export async function acknowledgeRelease(
  request: APIRequestContext,
  url: string,
  headers: Record<string, string>,
  command: Released | null | undefined,
) {
  const release = command?.secretRelease;
  if (!command || !release?.values) return;
  const response = await request.post(url, {
    headers,
    data: {
      acknowledged: [
        {
          operationId: command.id,
          epoch: release.epoch,
          digest: release.digest,
        },
      ],
      redeliver: [],
    },
  });
  expect(response.status(), await response.text()).toBe(204);
}
