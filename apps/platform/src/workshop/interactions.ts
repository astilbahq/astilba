import { expect, userEvent, waitFor, within } from "storybook/test";

// Native form names stay stable when the workshop changes language.
export async function fill(canvas: HTMLElement, name: string, value: string) {
  const input = await waitFor(async () => {
    const control = canvas.querySelector<HTMLInputElement>(
      `input[name="${name}"]`,
    );
    await expect(control).not.toBeNull();
    return control!;
  });
  await userEvent.clear(input);
  await userEvent.type(input, value);
  return input;
}

export async function submit(canvas: HTMLElement) {
  await userEvent.click(
    canvas.querySelector<HTMLButtonElement>('button[type="submit"]')!,
  );
}

export async function credentials(canvas: HTMLElement) {
  await fill(canvas, "email", "alex@example.test");
  await fill(canvas, "password", "correct horse battery staple");
  await submit(canvas);
}

export async function authenticationCode(canvas: HTMLElement) {
  await userEvent.type(within(canvas).getAllByRole("textbox")[0], "123456");
  await submit(canvas);
}
