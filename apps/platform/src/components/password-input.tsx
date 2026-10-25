import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../styled-system/css";
import { passwordInput } from "../../styled-system/recipes";
import { IconButton } from "./icon-button";
import { Icon } from "./icons";
import { Input, type InputProps } from "./input";
import { Tooltip } from "./tooltip";

export type PasswordInputProps = Omit<InputProps, "type">;

export function PasswordInput({
  id,
  disabled,
  className,
  ...props
}: PasswordInputProps) {
  const { t } = useTranslation("common");
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const [visible, setVisible] = useState(false);
  const styles = passwordInput();
  const toggleLabel = visible
    ? t(($) => $.password.hide)
    : t(($) => $.password.show);

  return (
    <div className={styles.root}>
      <Input
        {...props}
        id={inputId}
        disabled={disabled}
        type={visible ? "text" : "password"}
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        className={cx(styles.input, className)}
      />
      <Tooltip content={toggleLabel}>
        <IconButton
          type="button"
          variant="ghost"
          size="sm"
          className={styles.toggle}
          aria-label={toggleLabel}
          aria-controls={inputId}
          disabled={disabled}
          onPointerDown={(event) => {
            if (event.button === 0) event.preventDefault();
          }}
          onClick={() => setVisible((current) => !current)}
        >
          <Icon>
            <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" />
            <circle cx="12" cy="12" r="3" />
            {visible && <path d="m3 3 18 18" />}
          </Icon>
        </IconButton>
      </Tooltip>
    </div>
  );
}
