const portableRawName = /^[A-Z_][A-Z0-9_]{0,127}$/u;

export const materializeStringRecord = (
  source: Readonly<Record<string, unknown>>,
  names: readonly string[]
): Readonly<Record<string, string | undefined>> => {
  // oxlint-disable-next-line typescript/no-unsafe-assignment -- The null-prototype record is populated only with checked own string values below.
  const output: Record<string, string | undefined> = Object.create(null);
  const folded = new Set<string>();

  for (const name of names) {
    if (!portableRawName.test(name)) {
      throw new TypeError("Unsupported raw source name");
    }
    const key = name.toUpperCase();
    if (folded.has(key)) {
      throw new TypeError("Case-folding source collision");
    }
    folded.add(key);

    const descriptor = Object.getOwnPropertyDescriptor(source, name);
    if (descriptor !== undefined) {
      if (!("value" in descriptor)) {
        throw new TypeError("Source values must be own data properties");
      }
      if (
        descriptor.value !== undefined &&
        typeof descriptor.value !== "string"
      ) {
        throw new TypeError("Source values must be strings");
      }
      // oxlint-disable-next-line typescript/no-unsafe-assignment -- The preceding branch narrows this own descriptor value to string | undefined.
      output[name] = descriptor.value;
    }
  }

  return Object.freeze(output);
};
