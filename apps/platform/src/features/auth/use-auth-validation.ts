import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { createAuthMessages } from "./messages";
import { createAuthValidation } from "./validation";

export function useAuthValidation() {
  const { t } = useTranslation("auth");
  return useMemo(() => createAuthValidation(createAuthMessages(t)), [t]);
}
