import { css } from "../../../styled-system/css";

// A heading or item focus moves to when the control that had it goes away.
export const focusTarget = css({ outline: "none" });
export const subheading = css({ fontWeight: "semibold" });
export const part = css({ display: "grid", gap: "3", minInlineSize: "0" });
// An account's identifiers: each label above its value, so long ARNs wrap at narrow widths.
export const facts = css({
  display: "grid",
  gridTemplateColumns: { base: "1fr", md: "max-content minmax(0, 1fr)" },
  columnGap: "4",
  rowGap: { base: "0.5", md: "1.5" },
  margin: "0",
  "& dt": { color: "subtle", fontSize: "secondary" },
  "& dd": {
    margin: "0",
    marginBlockEnd: { base: "2", md: "0" },
    minInlineSize: "0",
  },
});
export const identifier = css({
  fontFamily: "mono",
  fontSize: "secondary",
  overflowWrap: "anywhere",
});
export const items = css({
  listStyle: "none",
  margin: "0",
  padding: "0",
  display: "grid",
  gap: "0",
  "& > li": {
    display: "grid",
    gap: "2",
    paddingBlock: "3",
    borderBlockEndWidth: "1px",
    borderBlockEndStyle: "solid",
    borderBlockEndColor: "border",
    minInlineSize: "0",
  },
  "& > li:last-child": { borderBlockEndWidth: "0" },
});
export const itemHeader = css({
  display: "flex",
  flexWrap: "wrap",
  alignItems: "baseline",
  justifyContent: "space-between",
  gap: "2",
});
// A choice between radio buttons, each with its own label.
export const choices = css({
  borderWidth: "0",
  padding: "0",
  margin: "0",
  minInlineSize: "0",
  display: "grid",
  gap: "3",
});
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
