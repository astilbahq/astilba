import { requireDockerHubMirror } from "../../../scripts/docker-hub-mirror.mjs";
import { pullImages } from "../../../scripts/fetch-attempts.mjs";

const words = (value) => value.split(/\s+/).filter(Boolean);
try {
  const images = words(process.env.INPUT_IMAGES);
  const docker = words(process.env.INPUT_DOCKER);
  await requireDockerHubMirror(images, docker);
  await pullImages(images, docker);
} catch (error) {
  console.error(`::error::${error.message}`);
  process.exitCode = 1;
}
