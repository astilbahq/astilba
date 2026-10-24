import { Menu as BaseMenu } from "@base-ui/react/menu";
import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { menu } from "../../styled-system/recipes";
import { CheckIcon } from "./icons";

export const MenuRoot = BaseMenu.Root;
export const MenuTrigger = BaseMenu.Trigger;
export const MenuRadioGroup = BaseMenu.RadioGroup;

export function MenuContent({
  className,
  side = "bottom",
  align = "end",
  sideOffset = 6,
  ...props
}: Omit<ComponentProps<typeof BaseMenu.Popup>, "className"> & {
  "aria-label": string;
  className?: string;
  side?: BaseMenu.Positioner.Props["side"];
  align?: BaseMenu.Positioner.Props["align"];
  sideOffset?: number;
}) {
  const styles = menu();
  return (
    <BaseMenu.Portal role="region" aria-label={props["aria-label"]}>
      <BaseMenu.Positioner
        positionMethod="fixed"
        side={side}
        align={align}
        sideOffset={sideOffset}
        className={styles.positioner}
      >
        <BaseMenu.Popup {...props} className={cx(styles.content, className)} />
      </BaseMenu.Positioner>
    </BaseMenu.Portal>
  );
}

export function MenuItem({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseMenu.Item>, "className"> & {
  className?: string;
}) {
  return <BaseMenu.Item {...props} className={cx(menu().item, className)} />;
}

export function MenuRadioItem({
  children,
  className,
  ...props
}: Omit<ComponentProps<typeof BaseMenu.RadioItem>, "className"> & {
  className?: string;
}) {
  const styles = menu();
  return (
    <BaseMenu.RadioItem
      closeOnClick
      {...props}
      className={cx(styles.item, className)}
    >
      {children}
      <BaseMenu.RadioItemIndicator className={styles.indicator}>
        <CheckIcon />
      </BaseMenu.RadioItemIndicator>
    </BaseMenu.RadioItem>
  );
}
