import type { ResourceLanguage } from "i18next";

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
const accented = "áƀçďéƒğħíĵķľḿńóƥɋŕšťúṽẅẋýžÁɃÇĎÉƑĞĦÍĴĶĽḾŃÓƤɊŔŠŤÚṼẄẊÝŽ";

function expand(message: string): string {
  // Keep interpolation and named React tags intact; expand only human text.
  const text = message
    .split(/(\{\{.*?\}\}|<[^>]+>)/g)
    .map((part) => {
      if (part.startsWith("{{") || part.startsWith("<")) return part;
      return part.replace(
        /[a-z]/gi,
        (letter) => accented[alphabet.indexOf(letter)],
      );
    })
    .join("");
  return `⟦${text}${" ~".repeat(Math.ceil(message.length / 8))}⟧`;
}

export function pseudolocalize(resources: ResourceLanguage): ResourceLanguage {
  return Object.fromEntries(
    Object.entries(resources).map(([key, value]) => [
      key,
      typeof value === "string" ? expand(value) : pseudolocalize(value),
    ]),
  );
}
