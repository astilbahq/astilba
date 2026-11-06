import { pullImages } from "../../../scripts/fetch-attempts.mjs";

const words = (value) => value.split(/\s+/).filter(Boolean);
try {
  await pullImages(
    words(process.env.INPUT_IMAGES),
    words(process.env.INPUT_DOCKER),
  );
} catch (error) {
  console.error(`::error::${error.message}`);
  process.exitCode = 1;
}
