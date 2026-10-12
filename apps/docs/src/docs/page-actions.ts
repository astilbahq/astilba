export type PageActionDestinationId =
  | "github"
  | "chatgpt"
  | "claude"
  | "t3-chat"
  | "copilot"
  | "cursor";

export interface PageActionDestination {
  href: string;
  id: PageActionDestinationId;
  label: string;
}

const createPromptUrl = (
  base: string,
  parameter: string,
  prompt: string,
  extraParameters?: Record<string, string>
): string => {
  const url = new URL(base);

  for (const [name, value] of Object.entries(extraParameters ?? {})) {
    url.searchParams.set(name, value);
  }

  url.searchParams.set(parameter, prompt);
  return url.href;
};

export const createPageActionDestinations = (
  markdownUrl: string,
  sourceUrl: string
): PageActionDestination[] => {
  const prompt = `Read ${markdownUrl}, I want to ask questions about it.`;

  return [
    {
      href: sourceUrl,
      id: "github",
      label: "GitHub",
    },
    {
      href: createPromptUrl("https://chatgpt.com/", "q", prompt, {
        hints: "search",
      }),
      id: "chatgpt",
      label: "ChatGPT",
    },
    {
      href: createPromptUrl("https://claude.ai/new", "q", prompt),
      id: "claude",
      label: "Claude",
    },
    {
      href: createPromptUrl("https://t3.chat/new", "q", prompt),
      id: "t3-chat",
      label: "T3 Chat",
    },
    {
      href: createPromptUrl("https://copilot.microsoft.com/", "q", prompt),
      id: "copilot",
      label: "Copilot",
    },
    {
      href: createPromptUrl("https://cursor.com/link/prompt", "text", prompt),
      id: "cursor",
      label: "Cursor",
    },
  ];
};
