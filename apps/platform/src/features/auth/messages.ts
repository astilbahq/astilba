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
    recoveryCodeRequired: t(($) => $.validation.recoveryCodeRequired),
    invalidRecoveryCode: t(($) => $.validation.invalidRecoveryCode),
    invalidFields: t(($) => $.validation.invalidFields),
    invalidValue: t(($) => $.validation.invalidValue),
    invalidPassword: t(($) => $.validation.invalidPassword),
    invalidCode: t(($) => $.validation.invalidCode),
    workspaceRequired: t(($) => $.validation.workspaceRequired),
    workspaceInvalid: t(($) => $.validation.workspaceInvalid),
    workspaceTooLong: ({ maximum }: { maximum: number }) =>
      t(($) => $.validation.workspaceTooLong, { count: maximum }),
    invalidEmailLink: t(($) => $.validation.invalidEmailLink),
    signupUnavailable: t(($) => $.validation.signupUnavailable),
    signupAccount: t(($) => $.validation.signupAccount),
    invalidIdentity: t(($) => $.validation.invalidIdentity),
    recentRequired: t(($) => $.validation.recentRequired),
    setupConflict: t(($) => $.validation.setupConflict),
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
