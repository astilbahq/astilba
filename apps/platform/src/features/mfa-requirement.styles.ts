import { css } from "../../styled-system/css";

// Members' names, email addresses and what they approved can be long and unbroken, so they wrap
// anywhere rather than widen the dialog.
export const approvals = css({ overflowWrap: "anywhere" });
