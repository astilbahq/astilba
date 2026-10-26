import { z } from "zod";
import { ApiError } from "../../api/client";
import {
  type FormValidation,
  validateWith,
} from "../../components/form-validation";
import type { AuthMessages } from "./messages";

const codeErrorFields = { invalid_credentials: ["code"] } as const;

function isRequestFailure(error: unknown) {
  return (
    !(error instanceof ApiError) ||
    ![
      "invalid_credentials",
      "invalid_input",
      "invitation_signin_required",
      "mfa_required",
      "recent_auth_required",
      "conflict",
    ].includes(error.detail.code)
  );
}

export function createAuthValidation(messages: AuthMessages) {
  const email = validateWith(
    z
      .string()
      .trim()
      .min(1, messages.emailRequired)
      .pipe(
        z.email({
          pattern: z.regexes.html5Email,
          error: messages.emailInvalid,
        }),
      )
      .refine((value) => new TextEncoder().encode(value).length <= 254, {
        error: messages.emailTooLong({ maximum: 254 }),
      }),
  );
  const password = validateWith(z.string().min(1, messages.passwordRequired));
  const newPassword = validateWith(
    z
      .string()
      .min(1, messages.passwordRequired)
      .refine((value) => Array.from(value).length >= 15, {
        error: messages.passwordTooShort({ minimum: 15 }),
      })
      .refine((value) => Array.from(value).length <= 128, {
        error: messages.passwordTooLong({ maximum: 128 }),
      }),
  );
  const namedValue = (required: string, invalid: string, tooLong: string) =>
    validateWith(
      z
        .string()
        .trim()
        .min(1, required)
        .refine((value) => Array.from(value).length <= 80, { error: tooLong })
        .refine(
          (value) =>
            !["\r", "\n", "\0"].some((character) => value.includes(character)),
          { error: invalid },
        ),
    );
  const name = namedValue(
    messages.nameRequired,
    messages.nameInvalid,
    messages.nameTooLong({ maximum: 80 }),
  );
  const workspace = namedValue(
    messages.workspaceRequired,
    messages.workspaceInvalid,
    messages.workspaceTooLong({ maximum: 80 }),
  );
  const code = validateWith(
    z
      .string()
      .trim()
      .regex(/^\d{6}$/, messages.codeRequired),
  );
  const recoveryCode = validateWith(
    z.string().trim().min(1, messages.recoveryCodeRequired),
  );

  function form(
    fields: FormValidation["fields"],
    credentialsMessage: string = messages.invalidCredentials,
    codeMessage: string = messages.invalidCode,
  ): FormValidation {
    return {
      fields,
      shouldToastError: isRequestFailure,
      message(error, field) {
        if (!(error instanceof ApiError)) return messages.requestFailed;
        switch (error.detail.code) {
          case "invalid_credentials":
            return credentialsMessage;
          case "invitation_signin_required":
            return messages.invitationSignIn;
          case "mfa_required":
            return messages.mfaRequired;
          case "rate_limited":
            return messages.rateLimited;
          case "recent_auth_required":
            return messages.recentRequired;
          case "conflict":
            return messages.setupConflict;
          case "invalid_input":
            if (!field) return messages.invalidFields;
            if (field === "email") return messages.emailInvalid;
            if (field === "password") return messages.invalidPassword;
            if (field === "code") return codeMessage;
            return messages.invalidValue;
          default:
            return messages.requestFailed;
        }
      },
    };
  }

  return {
    signIn: {
      ...form({ email, password }),
      shouldToastError: (error: unknown) =>
        isRequestFailure(error) ||
        (error instanceof ApiError &&
          error.detail.code === "invalid_credentials"),
    },
    mfa: {
      ...form({ code }, messages.invalidCode),
      errorFields: codeErrorFields,
    },
    recovery: {
      ...form(
        { code: recoveryCode },
        messages.invalidRecoveryCode,
        messages.invalidRecoveryCode,
      ),
      errorFields: codeErrorFields,
    },
    identity: (mfaEnabled: boolean, recovery: boolean) =>
      form(
        mfaEnabled
          ? { password, code: recovery ? recoveryCode : code }
          : { password },
        messages.invalidIdentity,
      ),
    setup: form({}, messages.invalidCode),
    resetRequest: form({ email }),
    resetPassword: form({ password: newPassword }, messages.invalidResetLink),
    invitation: form(
      { name, password: newPassword },
      messages.invalidInvitation,
    ),
    acceptInvitation: form({}, messages.invalidInvitation),
    emailVerification: form({}, messages.invalidEmailLink),
    signup: (existing: boolean): FormValidation => {
      const base = form(
        existing ? { workspace } : { workspace, name, password: newPassword },
        messages.invalidInvitation,
      );
      return {
        ...base,
        shouldToastError: (error) =>
          isRequestFailure(error) &&
          !(
            error instanceof ApiError &&
            ["conflict", "forbidden"].includes(error.detail.code)
          ),
        message(error, field) {
          if (error instanceof ApiError) {
            if (error.detail.code === "forbidden")
              return messages.signupAccount;
            if (error.detail.code === "conflict")
              return messages.signupUnavailable;
          }
          return base.message(error, field);
        },
      };
    },
  };
}
