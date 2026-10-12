type DisabledButton = Pick<HTMLButtonElement, "disabled" | "toggleAttribute">;

export const setButtonDisabled = (
  button: DisabledButton,
  disabled: boolean
): void => {
  button.disabled = disabled;
  button.toggleAttribute("data-disabled", disabled);
};
