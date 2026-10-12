import { docsUrl } from "./urls";

const AGENT_SETUP_PROMPT_URL = docsUrl("/agent-setup/prompt.md");
export const AGENT_SETUP_COPY_TEXT = `Fetch ${AGENT_SETUP_PROMPT_URL} and follow its instructions.`;
