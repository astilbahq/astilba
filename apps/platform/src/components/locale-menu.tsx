import { useTranslation } from "react-i18next";
import { isLocale, locales } from "../i18n/locale";
import { useLocale } from "../i18n/provider";
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

export function LocaleMenu({ scopeClassName }: { scopeClassName?: string }) {
  const { t } = useTranslation("common");
  const { preference, setPreference } = useLocale();
  if (locales.length < 2) return null;
  const label = t(($) => $.language.label);
  return (
    <MenuRoot>
      <Tooltip
        content={label}
        side="bottom"
        align="end"
        className={scopeClassName}
      >
        <MenuTrigger
          render={<IconButton aria-label={label} className={scopeClassName} />}
        >
          <Icon>
            <circle cx="12" cy="12" r="9" />
            <path d="M3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18Z" />
          </Icon>
        </MenuTrigger>
      </Tooltip>
      <MenuContent aria-label={label} className={scopeClassName}>
        <MenuRadioGroup
          value={preference}
          onValueChange={(value) => {
            if (value === "auto" || isLocale(value)) setPreference(value);
          }}
        >
          <MenuRadioItem value="auto">
            {t(($) => $.language.automatic)}
          </MenuRadioItem>
          {locales.map((locale) => (
            <MenuRadioItem key={locale.code} value={locale.code}>
              <span lang={locale.language} dir="ltr">
                {locale.name}
              </span>
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
      </MenuContent>
    </MenuRoot>
  );
}
