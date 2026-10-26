import { css } from "../../styled-system/css";

export const fields = css({
  display: "grid",
  gap: "5",
  borderWidth: "0",
  padding: "0",
  margin: "0",
  minInlineSize: "0",
  maxInlineSize: "2xl",
  "& > legend": { fontWeight: "semibold", marginBlockEnd: "4" },
});
export const preview = css({
  display: "grid",
  gap: "3",
  minInlineSize: "0",
});

export const commit = css({ overflowWrap: "anywhere" });
