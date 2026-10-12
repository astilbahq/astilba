const CONTRACT_ID_MIN_BYTES = 3;
const CONTRACT_ID_MAX_BYTES = 255;
const DNS_LABEL_MAX_BYTES = 63;

const LOCAL_ID = /^[a-z][A-Za-z0-9]{0,63}$/u;
const RAW_SOURCE_NAME = /^[A-Z_][A-Z0-9_]{0,127}$/u;
const DNS_LABEL = /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/u;

function isAscii(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.codePointAt(index);
    if (code === undefined || code > 0x7f) {
      return false;
    }
  }
  return true;
}

export function isContractId(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    !isAscii(value) ||
    value.length < CONTRACT_ID_MIN_BYTES ||
    value.length > CONTRACT_ID_MAX_BYTES
  ) {
    return false;
  }

  const labels = value.split(".");
  if (labels.length < 2) {
    return false;
  }

  return labels.every(
    (label) => label.length <= DNS_LABEL_MAX_BYTES && DNS_LABEL.test(label)
  );
}

export function isLocalId(value: unknown): value is string {
  return typeof value === "string" && LOCAL_ID.test(value);
}

export function isConsumerId(value: unknown): value is string {
  return isLocalId(value);
}

export function isOutputName(value: unknown): value is string {
  return isLocalId(value);
}

export function isRawSourceName(value: unknown): value is string {
  return typeof value === "string" && RAW_SOURCE_NAME.test(value);
}

export function asciiCaseFold(value: string): string {
  let folded = "";
  for (let index = 0; index < value.length; index += 1) {
    const code = value.codePointAt(index);
    if (code === undefined) {
      return folded;
    }
    folded +=
      code >= 0x41 && code <= 0x5a
        ? String.fromCodePoint(code + 0x20)
        : value[index];
  }
  return folded;
}

export function hasAsciiCaseFoldCollision(values: readonly string[]): boolean {
  const seen = new Set<string>();
  for (const value of values) {
    const folded = asciiCaseFold(value);
    if (seen.has(folded)) {
      return true;
    }
    seen.add(folded);
  }
  return false;
}
