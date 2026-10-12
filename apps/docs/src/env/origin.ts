import { ASTILBA_ORIGIN } from "../docs/urls.ts";

type OriginEntry<TConfiguration extends object> = {
  [TKey in keyof TConfiguration]-?: TConfiguration[TKey] extends
    | string
    | undefined
    ? TKey
    : never;
}[keyof TConfiguration] &
  string;

type TargetCheck<TConfiguration> =
  | Readonly<{ ok: false }>
  | Readonly<{ ok: true; value: TConfiguration }>;

export const resolveCanonicalOrigin = <TConfiguration extends object>(
  sourceName: string,
  result: TargetCheck<TConfiguration>,
  entry: OriginEntry<TConfiguration>
): string | undefined => {
  if (!result.ok) {
    throw new Error(
      `${sourceName} must be a valid public HTTPS origin. Set ${sourceName} to ${ASTILBA_ORIGIN}.`
    );
  }

  const origin = result.value[entry] as string | undefined;

  if (origin !== undefined && origin !== ASTILBA_ORIGIN) {
    throw new Error(
      `${sourceName} must use the canonical deployed origin ${ASTILBA_ORIGIN}.`
    );
  }

  return origin;
};
