import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { tabs } from "../../styled-system/recipes";

export function TabsRoot({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseTabs.Root>, "className"> & {
  className?: string;
}) {
  return <BaseTabs.Root {...props} className={cx(tabs().root, className)} />;
}
export function TabsList({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseTabs.List>, "className"> & {
  className?: string;
}) {
  return <BaseTabs.List {...props} className={cx(tabs().list, className)} />;
}
export function TabsTab({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseTabs.Tab>, "className"> & {
  className?: string;
}) {
  return <BaseTabs.Tab {...props} className={cx(tabs().tab, className)} />;
}
export function TabsPanel({
  className,
  ...props
}: Omit<ComponentProps<typeof BaseTabs.Panel>, "className"> & {
  className?: string;
}) {
  return <BaseTabs.Panel {...props} className={cx(tabs().panel, className)} />;
}
