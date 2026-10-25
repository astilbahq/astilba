import { Button, type ButtonProps } from "./button";

export type IconButtonProps = Omit<ButtonProps, "shape"> & {
  "aria-label": string;
};

export function IconButton({
  size = "sm",
  variant = "ghost",
  ...props
}: IconButtonProps) {
  return (
    <Button
      type="button"
      {...props}
      size={size}
      variant={variant}
      shape="square"
    />
  );
}
