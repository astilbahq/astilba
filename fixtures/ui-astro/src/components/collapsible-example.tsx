import { Collapsible } from "@astilba/ui/collapsible";

export const CollapsibleExample = () => (
  <Collapsible.Root>
    <Collapsible.Trigger>Deployment details</Collapsible.Trigger>
    <Collapsible.Panel>Deployed from a verified artifact.</Collapsible.Panel>
  </Collapsible.Root>
);
