import { css } from "../../styled-system/css";

export const headingAnchorCopyStyles = {
  host: css({
    display: "contents",
  }),
  icon: css({
    display: "block",
    inlineSize: "1.125rem",
    blockSize: "1.125rem",
  }),
  status: css({
    position: "absolute",
    inlineSize: "1px",
    blockSize: "1px",
    padding: 0,
    margin: "-1px",
    overflow: "hidden",
    clip: "rect(0, 0, 0, 0)",
    whiteSpace: "nowrap",
    border: 0,
  }),
};
