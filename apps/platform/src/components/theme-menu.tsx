import { useContext } from "react";
import { ThemeContext, type ThemePreference } from "../theme";
import { IconButton } from "./icon-button";
import { Icon } from "./icons";
import {
  MenuContent,
  MenuRadioGroup,
  MenuRadioItem,
  MenuRoot,
  MenuTrigger,
} from "./menu";
import { Tooltip } from "./tooltip";

function ThemeIcon({ mode }: { mode: ThemePreference }) {
  return (
    <Icon>
      {mode === "light" ? (
        <>
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5" />
        </>
      ) : mode === "dark" ? (
        <path d="M20.5 13A8.5 8.5 0 0 1 11 3.5 8.5 8.5 0 1 0 20.5 13Z" />
      ) : (
        <>
          <rect x="3" y="4" width="18" height="13" rx="2" />
          <path d="M8 21h8m-4-4v4" />
        </>
      )}
    </Icon>
  );
}

const options = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
] as const;

export function ThemeMenu({ scopeClassName }: { scopeClassName?: string }) {
  const { preference, setPreference } = useContext(ThemeContext);
  return (
    <MenuRoot>
      <Tooltip
        content="Appearance"
        side="bottom"
        align="end"
        className={scopeClassName}
      >
        <MenuTrigger
          render={
            <IconButton aria-label="Appearance" className={scopeClassName} />
          }
        >
          <ThemeIcon mode={preference} />
        </MenuTrigger>
      </Tooltip>
      <MenuContent aria-label="Appearance" className={scopeClassName}>
        <MenuRadioGroup value={preference} onValueChange={setPreference}>
          {options.map(({ value, label }) => (
            <MenuRadioItem key={value} value={value} closeOnClick={false}>
              <ThemeIcon mode={value} />
              {label}
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
      </MenuContent>
    </MenuRoot>
  );
}
