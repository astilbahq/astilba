import { jsonResponse, mcpServerCard } from "../../../discovery";

export const prerender = true;

export const GET = (): Response => jsonResponse(mcpServerCard);
