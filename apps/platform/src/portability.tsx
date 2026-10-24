import { useState } from "react";
import { client, result } from "./api/client";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";

export function WorkspaceExport({ workspaceId }: { workspaceId: string }) {
  const action = useAction();
  const [saved, setSaved] = useState(false);
  return (
    <section className={s.panel}>
      <h2>Export workspace for transfer</h2>
      <p>
        The encrypted archive includes configuration, credentials, history and
        server identities. Keep its passphrase separately. Accounts and running
        application data are excluded; use your backup destination for
        application data.
      </p>
      <p>
        Confirm your identity in account security before exporting. Imported
        servers stay blocked until you confirm the new controller locally on
        each host.
      </p>
      <ErrorNotice error={action.error} />
      <Form
        action={action}
        label="Encrypted workspace export"
        onSubmit={(data, form) =>
          action.run(async () => {
            const archive = await result(
              client.POST("/api/v1/workspaces/{workspaceId}/export", {
                params: { path: { workspaceId } },
                body: { passphrase: text(data, "passphrase") },
              }),
            );
            const raw = Uint8Array.from(atob(archive.archive), (c) =>
              c.charCodeAt(0),
            );
            const url = URL.createObjectURL(
              new Blob([raw], { type: "application/octet-stream" }),
            );
            const link = document.createElement("a");
            link.href = url;
            link.download = "astilba-workspace.astilba";
            link.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
            form.reset();
            setSaved(true);
          })
        }
      >
        <Field
          name="passphrase"
          label="Archive passphrase (at least 20 characters)"
          type="password"
          minLength={20}
          autoComplete="new-password"
        />
        <BusyButton busy={action.isPending}>
          Download encrypted workspace
        </BusyButton>
      </Form>
      {saved && (
        <p role="status">
          Encrypted archive downloaded. Keep the passphrase safely; Astilba
          cannot recover it.
        </p>
      )}
    </section>
  );
}

export function WorkspaceImport({ hosted }: { hosted: boolean }) {
  const action = useAction();
  const [done, setDone] = useState(false);
  return (
    <details className={s.panel}>
      <summary>Import an encrypted workspace</summary>
      <p>
        This creates a new workspace with its original resource identities. You
        become its owner. Automatic Git/build policies are disabled until you
        review them. Each server requires local owner confirmation before it can
        accept commands.
      </p>
      <ErrorNotice error={action.error} />
      <Form
        action={action}
        label="Import workspace"
        onSubmit={(data, form) =>
          action.run(async () => {
            const file = data.get("archive");
            if (!(file instanceof File) || !file.size || file.size > 33_554_484)
              throw new Error(
                "Choose an Astilba archive no larger than 32 MiB.",
              );
            const bytes = new Uint8Array(await file.arrayBuffer());
            let binary = "";
            for (let at = 0; at < bytes.length; at += 32768)
              binary += String.fromCharCode(...bytes.subarray(at, at + 32768));
            await result(
              client.POST("/api/v1/workspaces/import", {
                body: {
                  archive: btoa(binary),
                  passphrase: text(data, "passphrase"),
                  invitation: hosted ? text(data, "invitation") : undefined,
                },
              }),
            );
            form.reset();
            setDone(true);
          })
        }
      >
        <label>
          Encrypted workspace archive
          <input
            type="file"
            name="archive"
            accept=".astilba,application/octet-stream"
            required
          />
        </label>
        <Field
          name="passphrase"
          label="Archive passphrase"
          type="password"
          minLength={20}
          autoComplete="off"
        />
        {hosted && (
          <Field
            name="invitation"
            label="Beta workspace invitation code"
            autoComplete="off"
          />
        )}
        <label>
          <input name="ownership" type="checkbox" required /> Make me the
          imported workspace owner and require new approval for automation.
        </label>
        <BusyButton busy={action.isPending}>Import workspace</BusyButton>
      </Form>
      {done && (
        <p role="status">
          Workspace imported. Select it from your workspace list, then open
          Servers to confirm each host transfer.
        </p>
      )}
    </details>
  );
}
