import { css } from "../../styled-system/css";
import { visuallyHidden } from "../styles/recipes";

export const headingAnchorCopyStyles = {
  host: css({
    display: "contents",
  }),
  icon: css({
    display: "block",
    inlineSize: "1.125rem",
    blockSize: "1.125rem",
  }),
  status: visuallyHidden,
};
