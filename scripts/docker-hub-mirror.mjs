// CI reads Docker Hub images through Google's public mirror, so its pulls never depend on Docker
// Hub's anonymous rate limit. See "Docker Hub mirror" in docs/continuous-integration.md.
import { runLogged } from "./fetch-attempts.mjs";

// As `docker info` lists it. .github/actions/docker-hub-mirror adds it to the runner's daemon,
// and experiments/durable-execution/host/bootstrap.sh to the native lanes' fixture daemon.
export const dockerHubMirror = "https://mirror.gcr.io/";

// Whether Docker reads the image from Docker Hub: its name has no registry, as Docker decides
// that, or names Docker Hub itself.
export function fromDockerHub(image) {
  const slash = image.indexOf("/");
  if (slash < 0) return true;
  const first = image.slice(0, slash);
  if (first === "docker.io" || first === "index.docker.io") return true;
  return !(first.includes(".") || first.includes(":") || first === "localhost");
}

// Refuses to pull a Docker Hub image through a daemon without the mirror, so a job that skips
// the mirror step fails at once rather than when Docker Hub next refuses it.
export async function requireDockerHubMirror(images, docker = ["docker"]) {
  if (!images.some(fromDockerHub)) return;
  const [command, ...args] = docker;
  const listed = await runLogged(
    command,
    [...args, "info", "--format", "{{json .RegistryConfig.Mirrors}}"],
    { captureStdout: true, timeoutMs: 60_000 },
  );
  const mirrors = JSON.parse(listed.trim() || "null") ?? [];
  if (!mirrors.includes(dockerHubMirror))
    throw new Error(
      `Docker reads ${images.filter(fromDockerHub).join(", ")} from Docker Hub, but its daemon lists ${JSON.stringify(mirrors)} as Docker Hub mirrors, not ${dockerHubMirror}. Add the ./.github/actions/docker-hub-mirror step before this one; see "Docker Hub mirror" in docs/continuous-integration.md.`,
    );
}
