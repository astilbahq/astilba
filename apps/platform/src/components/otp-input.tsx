import { DirectionProvider } from "@base-ui/react/direction-provider";
import { OTPField } from "@base-ui/react/otp-field";
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../styled-system/css";
import {
  input,
  type InputVariantProps,
  otpInput,
} from "../../styled-system/recipes";

export type OTPInputProps = Omit<
  ComponentProps<typeof OTPField.Root>,
  "children" | "className" | "dir"
> &
  InputVariantProps & { className?: string; placeholder?: string };

export function OTPInput({
  length,
  variant,
  size,
  className,
  placeholder,
  ...props
}: OTPInputProps) {
  const { t } = useTranslation("common");
  const styles = otpInput();
  return (
    <DirectionProvider direction="ltr">
      <OTPField.Root
        {...props}
        length={length}
        dir="ltr"
        className={cx(styles.root, className)}
      >
        {Array.from({ length }, (_, index) => (
          <OTPField.Input
            key={index}
            aria-label={
              index === 0
                ? undefined
                : t(($) => $.otp.character, { position: index + 1, length })
            }
            autoCapitalize="none"
            placeholder={placeholder}
            className={cx(input({ variant, size }), styles.input)}
            // Base UI 1.9 exposes computed invalidity through state, while its
            // default slot aria-invalid only includes explicit/server errors.
            render={(inputProps, state) => (
              <input
                {...inputProps}
                aria-invalid={
                  (!state.disabled && state.valid === false) || undefined
                }
              />
            )}
          />
        ))}
      </OTPField.Root>
    </DirectionProvider>
  );
}
