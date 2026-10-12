import { ScrollArea } from "@astilba/ui/scroll-area";

export const ScrollAreaExample = () => (
  <ScrollArea.Root style={{ blockSize: "8rem", inlineSize: "18rem" }}>
    <ScrollArea.Viewport data-scroll-viewport fade="block">
      <ScrollArea.Content>
        <a href="#first">First release</a>
        <div style={{ blockSize: "16rem" }} />
        <a href="#latest">Latest release</a>
      </ScrollArea.Content>
    </ScrollArea.Viewport>
    <ScrollArea.Scrollbar>
      <ScrollArea.Thumb />
    </ScrollArea.Scrollbar>
  </ScrollArea.Root>
);
