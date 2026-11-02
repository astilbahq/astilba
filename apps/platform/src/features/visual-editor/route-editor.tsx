import { useState } from "react";
import type { Schema } from "../../api/client";
import { Button } from "../../components/button";
import { Input } from "../../components/input";
import { Label } from "../../components/label";
import { Select } from "../../components/select";
import { Textarea } from "../../components/textarea";
import * as s from "../../styles";
import { Form, text, useAction } from "../../ui";

type Spec = Schema["Specification"];

export function RouteEditor({
  spec,
  change,
  write,
}: {
  spec: Spec;
  change: (v: Spec) => void;
  write: boolean;
}) {
  const action = useAction();
  const [edit, setEdit] = useState<string>();
  const route = spec.routes?.find((r) => r.id === edit);
  return (
    <details>
      <summary>HTTP and TCP routes</summary>
      <ul className={s.list}>
        {spec.routes?.map((r) => (
          <li key={r.id}>
            {r.domain} · {r.protocol.toUpperCase()} · {r.tls}
            {write && (
              <>
                <Button type="button" onClick={() => setEdit(r.id)}>
                  Edit route
                </Button>
                <Button
                  type="button"
                  onClick={() =>
                    change({
                      ...spec,
                      routes: spec.routes?.filter((x) => x.id !== r.id),
                    })
                  }
                >
                  Remove route
                </Button>
              </>
            )}
          </li>
        ))}
      </ul>
      {write && (
        <Form
          key={edit ?? "new"}
          action={action}
          label="Route properties"
          onSubmit={(d, form) => {
            const protocol = text(d, "protocol") as "http" | "tcp";
            const headers = Object.fromEntries(
              text(d, "headers")
                .split("\n")
                .filter(Boolean)
                .map((line) => {
                  const i = line.indexOf(":");
                  return [line.slice(0, i).trim(), line.slice(i + 1).trim()];
                }),
            );
            const next: Schema["Route"] = {
              id: edit ?? crypto.randomUUID(),
              targetId: text(d, "target"),
              domain: text(d, "domain"),
              port: Number(text(d, "port")),
              protocol,
              listenPort:
                protocol === "tcp" ? Number(text(d, "listenPort")) : 0,
              tls: text(d, "tls") as Schema["Route"]["tls"],
              allowCidrs: text(d, "cidrs")
                .split(/[\n,]/)
                .map((v) => v.trim())
                .filter(Boolean),
              ...(protocol === "http"
                ? {
                    advanced: {
                      responseHeaders: headers,
                      stripPrefix: text(d, "prefix"),
                      requestsPerSecond: Number(text(d, "rate") || 0),
                    },
                  }
                : {}),
            };
            change({
              ...spec,
              routes: [
                ...(spec.routes ?? []).filter((r) => r.id !== edit),
                next,
              ],
            });
            setEdit(undefined);
            form.reset();
          }}
        >
          <Label>
            Target service
            <Select name="target" required defaultValue={route?.targetId}>
              {spec.applications
                .filter((a) => a.kind !== "worker")
                .map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
            </Select>
          </Label>
          <Label>
            Protocol
            <Select name="protocol" defaultValue={route?.protocol ?? "http"}>
              <option value="http">HTTP</option>
              <option value="tcp">TCP</option>
            </Select>
          </Label>
          <Label>
            Domain
            <Input name="domain" defaultValue={route?.domain} required />
          </Label>
          <Label>
            Container port
            <Input
              name="port"
              type="number"
              min={1}
              max={65535}
              defaultValue={route?.port ?? 8080}
              required
            />
          </Label>
          <Label>
            Host port (TCP only)
            <Input
              name="listenPort"
              type="number"
              min={1024}
              max={65535}
              defaultValue={route?.listenPort || 15432}
            />
          </Label>
          <Label>
            TLS policy
            <Select name="tls" defaultValue={route?.tls ?? "managed"}>
              <option value="managed">Managed certificate (HTTP)</option>
              <option value="passthrough">Pass through TLS (TCP)</option>
              <option value="off">Unencrypted</option>
            </Select>
          </Label>
          <Label>
            Allowed client networks (CIDRs, required for TCP)
            <Textarea
              name="cidrs"
              defaultValue={route?.allowCidrs.join("\n") ?? ""}
            />
          </Label>
          <details>
            <summary>Advanced HTTP settings</summary>
            <Label>
              Strip path prefix
              <Input
                name="prefix"
                defaultValue={route?.advanced?.stripPrefix ?? ""}
              />
            </Label>
            <Label>
              Requests per second (0 disables limit)
              <Input
                name="rate"
                type="number"
                min={0}
                max={10000}
                defaultValue={route?.advanced?.requestsPerSecond ?? 0}
              />
            </Label>
            <Label>
              Response headers (one Name: Value per line)
              <Textarea
                name="headers"
                defaultValue={Object.entries(
                  route?.advanced?.responseHeaders ?? {},
                )
                  .map(([k, v]) => `${k}: ${v}`)
                  .join("\n")}
              />
            </Label>
            <small>
              Only X-* headers, Content-Security-Policy and Referrer-Policy are
              accepted. Host files, templates and provider references are
              unavailable.
            </small>
          </details>
          <Button type="submit">Stage route</Button>
          {edit && (
            <Button type="button" onClick={() => setEdit(undefined)}>
              Cancel route edit
            </Button>
          )}
        </Form>
      )}
    </details>
  );
}
