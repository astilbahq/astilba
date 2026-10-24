import { Tooltip as BaseTooltip } from "@base-ui/react/tooltip";
import type { ReactElement, ReactNode } from "react";
import { cx } from "../../styled-system/css";
import { tooltip } from "../../styled-system/recipes";

export const TooltipProvider = BaseTooltip.Provider;

export function Tooltip({
  children,
  content,
  className,
  side = "top",
  align = "center",
}: {
  children: ReactElement;
  content: ReactNode;
  className?: string;
  side?: BaseTooltip.Positioner.Props["side"];
  align?: BaseTooltip.Positioner.Props["align"];
}) {
  const styles = tooltip();
  return (
    <BaseTooltip.Root>
      <BaseTooltip.Trigger render={children} />
      <BaseTooltip.Portal>
        <BaseTooltip.Positioner
          positionMethod="fixed"
          side={side}
          align={align}
          sideOffset={6}
          className={styles.positioner}
        >
          <BaseTooltip.Popup
            aria-hidden="true"
            className={cx(styles.content, className)}
          >
            {content}
          </BaseTooltip.Popup>
        </BaseTooltip.Positioner>
      </BaseTooltip.Portal>
    </BaseTooltip.Root>
  );
}
