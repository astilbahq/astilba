import { queryOptions } from "@tanstack/react-query";
import { loadSession } from "./client";

export const sessionOptions = queryOptions({
  queryKey: ["session"],
  queryFn: loadSession,
});
