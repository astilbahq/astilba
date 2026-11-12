import { css } from "../../../styled-system/css";

export const intro = css({ display: "grid", gap: "1.5", minInlineSize: "0" });
export const part = css({ display: "grid", gap: "4", minInlineSize: "0" });
export const focusTarget = css({ outline: "none" });
export const primary = css({ display: "block" });
export const metadata = css({
  display: "block",
  color: "subtle",
  fontSize: "secondary",
  fontWeight: "normal",
});
export const nowrap = css({ whiteSpace: "nowrap" });
export const items = css({
  listStyle: "none",
  margin: "0",
  padding: "0",
  display: "grid",
  gap: "1",
  overflowWrap: "anywhere",
});
export const more = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "center",
  gap: "3",
});
export const table = css({
  overflowInline: "auto",
  "& table": { inlineSize: "full", borderCollapse: "collapse" },
  "& th, & td": {
    textAlign: "start",
    verticalAlign: "top",
    padding: "2",
    borderBlockEndWidth: "1px",
    borderBlockEndStyle: "solid",
    borderBlockEndColor: "border",
  },
  "& th": { fontWeight: "medium" },
});
