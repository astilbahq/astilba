import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";

type Key = Schema["BackupStorageKey"];

// The fixed codes management records for an AWS failure, an account check or a key's revocation
// (docs/backups.md#managed-keys). The provider's own message is never recorded.
const failures = {
  credential_refused: "credentialRefused",
  access_denied: "accessDenied",
  not_found: "notFound",
  not_admin: "notAdmin",
  account_changed: "accountChanged",
  timeout: "timeout",
  unavailable: "unavailable",
  superseded: "superseded",
  user_missing: "userMissing",
  delete_conflict: "deleteConflict",
  not_listed: "notListed",
} as const;

// Turns a managed key's or storage account's machine values into plain words. Codes are an open
// set, so one added by a newer server is named as it is.
export function useBackupStorageText() {
  const { t, i18n } = useTranslation("backup-storage");
  return {
    date: (value: string) =>
      new Date(value).toLocaleString(i18n.resolvedLanguage),
    failure(code: string) {
      if (!Object.hasOwn(failures, code))
        return t(($) => $.failure.other, { code });
      const key = failures[code as keyof typeof failures];
      return t(($) => $.failure[key]);
    },
    purpose: (purpose: Key["purpose"]) =>
      purpose === "server"
        ? t(($) => $.key.purpose.server)
        : t(($) => $.key.purpose.recovery),
    state(state: Key["state"]) {
      switch (state) {
        case "minting":
          return t(($) => $.key.state.minting);
        case "active":
          return t(($) => $.key.state.active);
        case "revoking":
          return t(($) => $.key.state.revoking);
        case "revoked":
          return t(($) => $.key.state.revoked);
      }
    },
  };
}
