import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";

type AccessRecord = Schema["SecretAccessRecord"];

const shortId = (id: string | undefined) => (id ?? "").slice(0, 8);

// Item kinds the catalogue names. Each has a label and an "all of this kind" form.
const kinds = {
  "secret-version": "secretVersion",
  "build-secret": "buildSecret",
  git: "git",
  "github-authorization": "githubAuthorization",
  registry: "registry",
  "backup-destination": "backupDestination",
  "backup-policy": "backupPolicy",
  "alert-webhook": "alertWebhook",
  "recovery-checkpoint": "recoveryCheckpoint",
  totp: "totp",
  "totp-pending": "totpPending",
} as const;

// Turns the API's machine values into plain words. Reasons and item kinds are open
// sets, so a value added by a newer server is shown as it is.
export function useSecretAccessText() {
  const { t, i18n } = useTranslation("secret-access");
  const kindKey = (value: string) =>
    Object.hasOwn(kinds, value) ? kinds[value as keyof typeof kinds] : null;
  const kind = (value: string) => {
    const key = kindKey(value);
    return key ? t(($) => $.kind[key]) : value;
  };
  // An item without an ID stands for every item of its kind.
  const allOfKind = (value: string) => {
    const key = kindKey(value);
    return key
      ? t(($) => $.allOfKind[key])
      : t(($) => $.item.allOther, { kind: value });
  };
  return {
    date: (value: string) =>
      new Date(value).toLocaleString(i18n.resolvedLanguage),
    purpose(purpose: AccessRecord["purpose"]) {
      switch (purpose) {
        case "seal":
          return t(($) => $.purpose.seal);
        case "deliver":
          return t(($) => $.purpose.deliver);
        case "operate":
          return t(($) => $.purpose.operate);
        case "inspect":
          return t(($) => $.purpose.inspect);
        case "copy":
          return t(($) => $.purpose.copy);
        case "export":
          return t(($) => $.purpose.export);
        case "recover":
          return t(($) => $.purpose.recover);
        case "rotate":
          return t(($) => $.purpose.rotate);
        case "authenticate":
          return t(($) => $.purpose.authenticate);
        case "token":
          return t(($) => $.purpose.token);
      }
    },
    // A principal reads as one name, with a detail line where one helps tell
    // two apart: the agent instance's key fingerprint, or a system job.
    principal({ principal }: AccessRecord): {
      name: string;
      detail?: string;
    } {
      switch (principal.kind) {
        // The email is present only while the account is a member.
        case "account":
          return {
            name:
              principal.accountEmail ??
              t(($) => $.principal.account, {
                id: shortId(principal.accountId),
              }),
          };
        case "automation":
          return {
            name:
              principal.automationName ??
              t(($) => $.principal.automation, {
                id: shortId(principal.automationId),
              }),
          };
        case "agent":
          return {
            name:
              principal.serverName ??
              t(($) => $.principal.server, {
                id: shortId(principal.serverId),
              }),
            detail: principal.instance
              ? t(($) => $.principal.instance, {
                  instance: principal.instance,
                })
              : undefined,
          };
        case "system":
          return { name: t(($) => $.principal.system), detail: principal.job };
        case "installation":
          return {
            name: t(($) => $.principal.installation),
            detail: principal.job,
          };
      }
    },
    item: (item: Schema["SecretAccessItem"]) =>
      !item.id
        ? allOfKind(item.kind)
        : item.name
          ? t(($) => $.item.named, { name: item.name, kind: kind(item.kind) })
          : t(($) => $.item.unnamed, {
              kind: kind(item.kind),
              id: shortId(item.id),
            }),
    more: (count: number) => t(($) => $.item.more, { count }),
    noItems: () => t(($) => $.item.none),
    reason(reason: string) {
      switch (reason) {
        case "first-release":
          return t(($) => $.reason.firstRelease);
        case "redelivery":
          return t(($) => $.reason.redelivery);
        case "unacknowledged":
          return t(($) => $.reason.unacknowledged);
        case "decision":
          return t(($) => $.reason.decision);
        case "installation-key-maintenance":
          return t(($) => $.reason.installationKeyMaintenance);
        case "write":
          return t(($) => $.reason.write);
        case "rewrap":
          return t(($) => $.reason.rewrap);
        case "sign-in":
          return t(($) => $.reason.signIn);
        case "start-up":
          return t(($) => $.reason.startUp);
        // Management records these when a request gives no reason of its own.
        case "operate":
        case "inspect":
        case "copy":
        case "export":
        case "recover":
          return t(($) => $.reason.standard);
        default:
          return reason;
      }
    },
    outcome(outcome: AccessRecord["outcome"]) {
      switch (outcome) {
        case "released":
          return t(($) => $.outcome.released);
        case "used":
          return t(($) => $.outcome.used);
        case "refused":
          return t(($) => $.outcome.refused);
        case "failed":
          return t(($) => $.outcome.failed);
        case "abandoned":
          return t(($) => $.outcome.abandoned);
        // Still open, or its end was never recorded: never read as success.
        case "unknown":
          return t(($) => $.outcome.unknown);
      }
    },
  };
}
