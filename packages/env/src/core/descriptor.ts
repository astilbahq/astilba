import {
  normalizePublicCodecDescriptor,
  normalizePortableCodecDescriptor,
} from "./codecs.ts";
import { ContractDefinitionError } from "./diagnostics.ts";
import { normalizeIntegerCodec, normalizeTextCodec } from "./server-codecs.ts";
import type { CodecDescriptor, PublicCodecDescriptor } from "./types.ts";

export const normalizeCodecDescriptor = (
  descriptor: CodecDescriptor
): CodecDescriptor => {
  if (typeof descriptor !== "object" || descriptor === null) {
    throw new ContractDefinitionError();
  }
  const kindDescriptor = Object.getOwnPropertyDescriptor(descriptor, "kind");
  if (kindDescriptor === undefined || !("value" in kindDescriptor)) {
    throw new ContractDefinitionError();
  }

  switch (kindDescriptor.value) {
    case "boolean":
    case "enum":
    case "json":
    case "origin":
    case "safe-integer":
    case "string":
    case "string-list": {
      return normalizePublicCodecDescriptor(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The checked discriminant selects the public codec family before normalization.
        descriptor as PublicCodecDescriptor
      );
    }
    case "opaque": {
      return normalizePortableCodecDescriptor(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The checked discriminant selects the opaque codec family before normalization.
        descriptor as Extract<CodecDescriptor, { kind: "opaque" }>
      );
    }
    case "integer": {
      return normalizeIntegerCodec(descriptor);
    }
    case "text": {
      return normalizeTextCodec(descriptor);
    }
    default: {
      throw new ContractDefinitionError();
    }
  }
};
