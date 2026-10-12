interface WebMcpTool {
  annotations: {
    readOnlyHint: boolean;
    untrustedContentHint: boolean;
  };
  description: string;
  execute: (input?: { offset?: number }) => Promise<string>;
  inputSchema: {
    additionalProperties: false;
    properties: {
      offset: {
        description: string;
        minimum: number;
        type: "integer";
      };
    };
    type: "object";
  };
  name: string;
}

interface WebMcpModelContext {
  provideContext?: (context: {
    tools: WebMcpTool[];
  }) => PromiseLike<void> | void;
  registerTool?: (tool: WebMcpTool) => PromiseLike<void> | void;
}

type WebMcpDocument = Document & {
  modelContext?: WebMcpModelContext;
};

type WebMcpNavigator = Navigator & {
  modelContext?: WebMcpModelContext;
};

type WebMcpRegistration = (tool: WebMcpTool) => PromiseLike<void> | void;

const MARKDOWN_CHUNK_LENGTH = 1200;
const markdownCharactersByPath = new Map<string, Promise<string[]>>();
const markdownLink = document.querySelector<HTMLLinkElement>(
  'link[rel="alternate"][type="text/markdown"]'
);

const getCurrentMarkdownPath = (): string => {
  const currentMarkdownLink = document.querySelector<HTMLLinkElement>(
    'link[rel="alternate"][type="text/markdown"]'
  );

  if (!currentMarkdownLink) {
    throw new Error("The current page does not advertise a Markdown version.");
  }

  return new URL(currentMarkdownLink.href).pathname;
};

const getMarkdownCharacters = async (
  markdownPath: string
): Promise<string[]> => {
  let charactersPromise = markdownCharactersByPath.get(markdownPath);

  if (!charactersPromise) {
    charactersPromise = (async () => {
      const response = await fetch(markdownPath, {
        headers: { Accept: "text/markdown" },
      });

      if (!response.ok) {
        throw new Error(
          `The page Markdown request failed with status ${response.status}.`
        );
      }

      return [...(await response.text())];
    })();
    markdownCharactersByPath.set(markdownPath, charactersPromise);
  }

  try {
    return await charactersPromise;
  } catch (error) {
    if (markdownCharactersByPath.get(markdownPath) === charactersPromise) {
      markdownCharactersByPath.delete(markdownPath);
    }
    throw error;
  }
};

const ignoreRegistrationResult = (
  register: () => PromiseLike<void> | void
): void => {
  try {
    void Promise.resolve(register()).catch(() => undefined);
  } catch {
    // WebMCP is progressive enhancement; registration must not affect the page.
  }
};

const getRegistration = (): WebMcpRegistration | undefined => {
  const currentModelContext = (document as WebMcpDocument).modelContext;

  if (typeof currentModelContext?.registerTool === "function") {
    return (tool) => currentModelContext.registerTool?.(tool);
  }

  const legacyModelContext = (navigator as WebMcpNavigator).modelContext;

  if (typeof legacyModelContext?.registerTool === "function") {
    return (tool) => legacyModelContext.registerTool?.(tool);
  }

  if (typeof legacyModelContext?.provideContext === "function") {
    return (tool) => legacyModelContext.provideContext?.({ tools: [tool] });
  }

  return undefined;
};

const registerTool = markdownLink ? getRegistration() : undefined;

if (registerTool) {
  ignoreRegistrationResult(() =>
    registerTool({
      annotations: {
        readOnlyHint: true,
        untrustedContentHint: false,
      },
      description:
        "Return one bounded chunk of the Markdown page open in this tab. Start at offset 0, then request the next offset reported in each result.",
      execute: async ({ offset = 0 } = {}) => {
        if (!Number.isSafeInteger(offset) || offset < 0) {
          throw new Error("Offset must be a non-negative integer.");
        }

        const markdownPath = getCurrentMarkdownPath();
        const characters = await getMarkdownCharacters(markdownPath);

        if (offset > characters.length) {
          throw new Error(
            `Offset ${offset} exceeds the page length of ${characters.length} characters.`
          );
        }

        const end = Math.min(offset + MARKDOWN_CHUNK_LENGTH, characters.length);
        const continuation =
          end < characters.length ? `Next offset: ${end}.` : "End of page.";

        return [
          `Markdown characters ${offset}–${end} of ${characters.length}.`,
          characters.slice(offset, end).join(""),
          continuation,
        ].join("\n\n");
      },
      inputSchema: {
        additionalProperties: false,
        properties: {
          offset: {
            description:
              "Zero-based character offset. Begin with 0 and continue from the offset reported by the previous result.",
            minimum: 0,
            type: "integer",
          },
        },
        type: "object",
      },
      name: "read_current_page_markdown",
    })
  );
}
