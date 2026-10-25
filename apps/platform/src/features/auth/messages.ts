import type { TFunction } from "i18next";

export function createAuthMessages(t: TFunction<"auth">) {
  return {
    emailRequired: t(($) => $.validation.emailRequired),
    emailInvalid: t(($) => $.validation.emailInvalid),
    emailTooLong: ({ maximum }: { maximum: number }) =>
      t(($) => $.validation.emailTooLong, { maximum }),
    passwordRequired: t(($) => $.validation.passwordRequired),
    passwordTooShort: ({ minimum }: { minimum: number }) =>
      t(($) => $.validation.passwordTooShort, { count: minimum }),
    passwordTooLong: ({ maximum }: { maximum: number }) =>
      t(($) => $.validation.passwordTooLong, { count: maximum }),
    nameRequired: t(($) => $.validation.nameRequired),
    nameInvalid: t(($) => $.validation.nameInvalid),
    nameTooLong: ({ maximum }: { maximum: number }) =>
      t(($) => $.validation.nameTooLong, { count: maximum }),
    codeRequired: t(($) => $.validation.codeRequired),
    invalidFields: t(($) => $.validation.invalidFields),
    invalidValue: t(($) => $.validation.invalidValue),
    invalidPassword: t(($) => $.validation.invalidPassword),
    invalidCode: t(($) => $.validation.invalidCode),
    invalidCredentials: t(($) => $.validation.invalidCredentials),
    invalidResetLink: t(($) => $.validation.invalidResetLink),
    invalidInvitation: t(($) => $.validation.invalidInvitation),
    invitationSignIn: t(($) => $.validation.invitationSignIn),
    mfaRequired: t(($) => $.validation.mfaRequired),
    rateLimited: t(($) => $.validation.rateLimited),
    requestFailed: t(($) => $.validation.requestFailed),
  };
}

export type AuthMessages = ReturnType<typeof createAuthMessages>;
