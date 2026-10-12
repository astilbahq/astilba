import { cacheContext } from "@astilba/cache/react-router";
import { href, Link } from "react-router";

import { allScenes } from "../../chaos/script";
import { NarrationTable, ProbePanel } from "../console";
import { narrate } from "../narrate";
import { factoryRunCount, readProbe, witnessProbe } from "../probe";
import { rescueIfArrived, worldContext } from "../world";
import type { Route } from "./+types/home";

export async function loader({ context }: Route.LoaderArgs) {
  const world = context.get(worldContext);
  const cache = context.get(cacheContext);
  const probe = await readProbe(cache);
  // The witness runs on every path, including the ones where the read above threw — that is
  // precisely when it is worth having. It can itself raise: it reads the store to answer, so a
  // scene that broke the store breaks the witness too, and the narration says so rather than
  // dressing the silence up as an absent key.
  const witness = await witnessProbe(cache);
  const armed = world.armed();
  const pending = world.pendingPurge();
  const arrived = rescueIfArrived(pending, witness, world.nowMs());
  if (arrived !== null) {
    world.rescueObserved(arrived);
  }
  return {
    armed: armed?.id,
    factoryRuns: factoryRunCount(),
    narration: narrate({
      armedForMs: armed === null ? null : world.nowMs() - armed.atMs,
      awaitingRescueForMs:
        arrived === null && pending !== null
          ? world.nowMs() - pending.atMs
          : null,
      busStats: world.busStats,
      coordinatorHeartbeatMs: world.coordinatorHeartbeatMs,
      dials: world.dials,
      events: world.events,
      filled: probe.filled,
      kvStats: world.kvStats,
      readMs: probe.readMs,
      rescue: world.lastRescue(),
      scene: null,
      witness,
    }),
    probe,
    registryName: world.registryName,
    scenes: allScenes().map((scene) => ({
      id: scene.id,
      premise: scene.premise,
      title: scene.title,
    })),
  };
}

export default function Home({ loaderData }: Route.ComponentProps) {
  const { armed, factoryRuns, narration, probe, registryName, scenes } =
    loaderData;
  return (
    <main
      style={{
        fontFamily: "system-ui, sans-serif",
        margin: "0 auto",
        maxWidth: "60rem",
        padding: "2rem 1rem",
      }}
    >
      <h1>@astilba/cache — the chaos demo</h1>
      <p>
        A real React Router v8 app on real Cloudflare bindings: a KV namespace
        for the durable tier, a Coordinator Durable Object for the registry and
        the invalidation bus. The cache is the five-liner — one name, the KV
        binding, the Coordinator namespace — and the name is the address:{" "}
        <code>{registryName}</code> is the Durable Object&rsquo;s name, the
        registry id, and the key namespace, all three.
      </p>
      <p>
        It is built <strong>once, at module scope</strong> — one cache per
        isolate, which is the placement the quickstart teaches — and it outlives
        every request on this page. That is worth pointing at, because this app
        is the reason it works: holding the published factory across a request
        boundary is what found the lifetime defect. The factory used to resolve
        the Coordinator namespace to a stub and hold it, and a Workers stub
        belongs to the request that minted it, so a cache that outlived a
        request could never touch its registry again. It now holds the{" "}
        <em>address</em> and mints a handle per use. The reader below is
        consequently the same reader across your reloads, and its state
        accumulates.
      </p>
      <p>
        Between the app and those bindings sit three wrappers this demo owns.
        They implement the platform&rsquo;s own interfaces and can be told to
        reject reads, reject writes, or refuse the socket. Nothing in the
        library knows they are there —{" "}
        <strong>there is no fault-injection code in the cache</strong>, and
        there is not going to be. A library that shipped a way to make itself
        fail would ship that way to everyone who ever deployed it.
      </p>
      <p>
        The factory has run <strong>{factoryRuns}</strong> time(s) in this
        isolate.
        {armed === undefined
          ? " Nothing is armed: this is the healthy system."
          : ` The ${armed} scene is armed.`}
      </p>

      <h2>The three faults</h2>
      <ul>
        {scenes.map((scene) => (
          <li key={scene.id} style={{ marginBottom: "0.8rem" }}>
            <Link to={href("/scene/:sceneId", { sceneId: scene.id })}>
              {scene.title}
            </Link>
            <br />
            <span style={{ color: "#555" }}>{scene.premise}</span>
          </li>
        ))}
      </ul>

      <ProbePanel probe={probe} />
      <NarrationTable narration={narration} />
    </main>
  );
}
