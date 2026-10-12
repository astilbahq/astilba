export type Measurement = Readonly<{
  iterations: number;
  maximumMs: number;
  meanMs: number;
  minimumMs: number;
  p95Ms: number;
  warmup: number;
}>;

const percentile = (sorted: readonly number[], fraction: number): number => {
  if (sorted.length === 0) {
    throw new RangeError("At least one sample is required");
  }
  const index = Math.min(
    sorted.length - 1,
    Math.ceil(sorted.length * fraction) - 1
  );
  return sorted[index] ?? 0;
};

export const measureWarm = async (
  operation: () => void | Promise<void>,
  options: Readonly<{
    iterations: number;
    now?: () => number;
    warmup: number;
  }>
): Promise<Measurement> => {
  if (
    !Number.isSafeInteger(options.warmup) ||
    options.warmup < 0 ||
    !Number.isSafeInteger(options.iterations) ||
    options.iterations < 1
  ) {
    throw new RangeError("Invalid measurement sample counts");
  }
  const now = options.now ?? (() => performance.now());

  for (let index = 0; index < options.warmup; index += 1) {
    await operation();
  }

  const samples: number[] = [];
  for (let index = 0; index < options.iterations; index += 1) {
    const started = now();
    await operation();
    samples.push(now() - started);
  }

  samples.sort((left, right) => left - right);
  const total = samples.reduce((sum, sample) => sum + sample, 0);

  return Object.freeze({
    iterations: options.iterations,
    maximumMs: samples.at(-1) ?? 0,
    meanMs: total / samples.length,
    minimumMs: samples[0] ?? 0,
    p95Ms: percentile(samples, 0.95),
    warmup: options.warmup,
  });
};
