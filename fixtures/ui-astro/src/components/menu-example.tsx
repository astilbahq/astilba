import { Menu } from "@astilba/ui/menu";

export const MenuExample = () => (
  <Menu.Root>
    <Menu.Trigger>More actions</Menu.Trigger>
    <Menu.Portal>
      <Menu.Positioner>
        <Menu.Popup>
          <Menu.Item label="Refresh">Refresh</Menu.Item>
          <Menu.LinkItem href="/docs" label="Read the docs">
            Read the docs
          </Menu.LinkItem>
        </Menu.Popup>
      </Menu.Positioner>
    </Menu.Portal>
  </Menu.Root>
);
