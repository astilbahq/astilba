import { expect, type Page } from "@playwright/test";

/**
 * Answers the dialog that a change to the password or two-factor authentication opens for
 * the account's current credential: the password, an authenticator code or a recovery code.
 */
export async function confirmCredential(
  page: Page,
  current: { password: string } | { code: string } | { recoveryCode: string },
) {
  const dialog = page.getByRole("alertdialog", {
    name: "Confirm your identity",
  });
  await expect(dialog).toBeVisible();
  if ("password" in current) {
    await dialog.getByLabel("Password", { exact: true }).fill(current.password);
  } else if ("code" in current) {
    await dialog
      .getByRole("group", { name: "Authentication code", exact: true })
      .getByRole("textbox")
      .first()
      .pressSequentially(current.code);
  } else {
    await dialog.getByRole("button", { name: "Use a recovery code" }).click();
    await dialog
      .getByLabel("Recovery code", { exact: true })
      .fill(current.recoveryCode);
  }
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(dialog).toHaveCount(0);
}
