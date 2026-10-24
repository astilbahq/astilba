// Keep complete messages and interpolation separate from rules. A future locale
// can supply this shape to createAuthValidation without global Zod configuration.
export const authMessages = {
  emailRequired: "Enter your email address.",
  emailInvalid: "Enter a valid email address.",
  emailTooLong: ({ maximum }: { maximum: number }) =>
    `Use an email address of ${maximum} bytes or fewer.`,
  passwordRequired: "Enter your password.",
  passwordTooShort: ({ minimum }: { minimum: number }) =>
    `Use at least ${minimum} characters for your password.`,
  passwordTooLong: ({ maximum }: { maximum: number }) =>
    `Use ${maximum} characters or fewer for your password.`,
  nameRequired: "Enter your name.",
  nameInvalid: "Enter a name without line breaks or control characters.",
  nameTooLong: ({ maximum }: { maximum: number }) =>
    `Use ${maximum} characters or fewer for your name.`,
  codeRequired: "Enter an authenticator or recovery code.",
  invalidFields: "Check the form details and try again.",
  invalidValue: "Check this value and try again.",
  invalidPassword: "Check your password and try again.",
  invalidCode: "Check your authenticator or recovery code and try again.",
  invalidCredentials: "The email or password is incorrect.",
  invalidResetLink: "This reset link is invalid or has expired.",
  invalidInvitation: "This invitation link is invalid or has expired.",
  invitationSignIn: "Sign in as the invited account before accepting.",
  rateLimited: "Too many attempts. Try again later.",
  requestFailed: "The request could not be completed. Try again.",
};

export type AuthMessages = typeof authMessages;
