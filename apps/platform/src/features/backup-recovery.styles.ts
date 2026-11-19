import { css } from "../../styled-system/css";

export const choices = css({
  borderWidth: "0",
  padding: "0",
  margin: "0",
  minInlineSize: "0",
  display: "grid",
  gap: "3",
});
export const heading = css({ fontWeight: "semibold" });
export const legend = css({ fontWeight: "semibold", marginBlockEnd: "2" });
export const choice = css({
  display: "flex",
  alignItems: "flex-start",
  gap: "3",
  cursor: "pointer",
  "& > input": { marginBlockStart: "1" },
});
export const choiceText = css({
  display: "grid",
  gap: "0.5",
  minInlineSize: "0",
});
export const identifier = css({
  fontFamily: "mono",
  fontSize: "secondary",
  color: "subtle",
  overflowWrap: "anywhere",
});
