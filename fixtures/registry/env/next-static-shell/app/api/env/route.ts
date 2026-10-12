import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";
import { NextResponse } from "next/server";

import { check } from "../../../.astilba/env/bootstrapDeployment.server.ts";
import { projection } from "../../../.astilba/env/browser/browser.deployment.ts";

export const dynamic = "force-dynamic";

const headers = { "Cache-Control": "private, no-store" };

const localOriginFromRequest = (request: Request): string | undefined => {
  const url = new URL(request.url);
  if (
    url.protocol !== "http:" ||
    (url.hostname !== "localhost" && url.hostname !== "127.0.0.1")
  ) {
    return undefined;
  }
  return url.origin;
};

export function GET(request: Request) {
  const checked = check(process.env);
  if (!checked.ok) {
    return NextResponse.json(
      { error: checked.diagnostics[0]?.code },
      { headers, status: 500 }
    );
  }
  const browserOrigin =
    checked.value.browserOrigin ?? localOriginFromRequest(request);
  if (browserOrigin === undefined) {
    return NextResponse.json(
      { error: "ENV_INVALID_VALUE" },
      { headers, status: 500 }
    );
  }
  return NextResponse.json(
    {
      audience: { origin: browserOrigin },
      consumer: projection.consumer,
      contract: projection.contract,
      lifecycle: projection.lifecycle,
      projection: projection.digest,
      protocol: BOOTSTRAP_PROTOCOL,
      values: { label: checked.value.label },
    },
    { headers }
  );
}
