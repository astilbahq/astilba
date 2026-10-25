import { z } from "zod";
import { ApiError } from "../../api/client";
import {
  type FormValidation,
  validateWith,
} from "../../components/form-validation";
import type { AuthMessages } from "./messages";

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
  const name = validateWith(
    z
      .string()
      .trim()
      .min(1, messages.nameRequired)
      .refine((value) => Array.from(value).length <= 80, {
        error: messages.nameTooLong({ maximum: 80 }),
      })
      .refine(
        (value) =>
          !["\r", "\n", "\0"].some((character) => value.includes(character)),
        {
          error: messages.nameInvalid,
        },
      ),
  );
  const code = validateWith(z.string().trim().min(1, messages.codeRequired));

  function form(
    fields: FormValidation["fields"],
    credentialsMessage: string = messages.invalidCredentials,
  ): FormValidation {
    return {
      fields,
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
          case "invalid_input":
            if (!field) return messages.invalidFields;
            if (field === "email") return messages.emailInvalid;
            if (field === "password") return messages.invalidPassword;
            if (field === "code") return messages.invalidCode;
            return messages.invalidValue;
          default:
            return messages.requestFailed;
        }
      },
    };
  }

  return {
    signIn: form({ email, password }),
    mfa: form({ code }, messages.invalidCode),
    resetRequest: form({ email }),
    resetPassword: form({ password: newPassword }, messages.invalidResetLink),
    invitation: form(
      { name, password: newPassword },
      messages.invalidInvitation,
    ),
    acceptInvitation: form({}, messages.invalidInvitation),
  };
}
