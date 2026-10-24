import type { ReactNode } from "react";
import { cx } from "../../../styled-system/css";
import { ThemeMenu } from "../../components/theme-menu";
import * as auth from "./auth.styles";

export function AuthFrame({ children }: { children: ReactNode }) {
  return (
    <>
      <div className={auth.appearance}>
        <ThemeMenu scopeClassName={auth.palette} />
      </div>
      <main id="main" className={cx(auth.palette, auth.page)}>
        <div className={auth.content}>{children}</div>
      </main>
    </>
  );
}
