# @astilba/cache — the chaos demo

A React Router v8 app on real Cloudflare bindings — a KV namespace for the durable tier, a
Coordinator Durable Object for the registry and the invalidation bus. It wires the hero five-liner
(`createWorkersCache`) and runs three faults past it — a KV backend kill, a bus dial loss, and a
Coordinator outage — each injected **demo-side** by wrappers that sit between the real bindings and
the library. The library carries no fault-injection surface. Every scene narrates what the system
actually did next to the bounds this deployment documents, and it asserts no SLO.

## Running it

Two rigs, and they are not equivalent — see the note below.

```sh
pnpm dev                                          # react-router dev (Vite + a Workers shim)

pnpm build                                        # emits build/server/{index.js,wrangler.json}
pnpm exec wrangler dev -c build/server/wrangler.json   # the built worker on real workerd
```

## Which rig establishes the live bus channel

`react-router dev` runs the app under Vite with a Workers shim rather than under workerd, and the
Durable Object WebSocket `hello` handshake does not complete there: the reader's channel stays
`never-established` across reloads (`explain().reader.channel`). That is not the library degrading —
it is the dev rig not carrying the live channel. Under it you are watching the two paths that do not
need the socket: the poll carrier and the fail-closed posture. The reader still converges — a purge
is learned by a live registry check, and a value is never served falsely fresh — it simply converges
without the bus.

Run the built worker on **workerd** (`wrangler dev` against `build/server/wrangler.json`, with the
real Coordinator DO + KV bindings) and the channel reaches `established` (E28): the dial completes,
the `hello` arrives, and the deaf-reader scene's `bus_dial_failed` / `never-established` witnesses
become reachable under an armed `bus-drop`. Measured on this build: `never-established` across reloads
in `react-router dev`; `established` from the first read on workerd.

## The instrument that holds that measurement

That last sentence is a measurement, so it is held by an instrument rather than by this paragraph.
Run it from the repository root:

```sh
pnpm -C apps/cache-demo run build        # emits build/server/{index.js,wrangler.json}
node scripts/demo-boot-witness.mjs # boots that build on workerd and reads the channel
```

Two steps, and they stay two. The witness boots a build; it does not make one — so on a clean
checkout, where `build/` does not exist, the first command is not optional. It is also how the CI job
`demo-boot-witness` (required) runs it, and the step boundary is deliberate: a build failure and a
witness failure are different findings, and one step would merge them into a single red. Skip the
build and the script says so by name and prints the command above, rather than letting the missing
path surface as a complaint from wrangler about an entry point.

What it asserts: the channel is read from `/status.json` — never from the rendered table, because a
markup change must not be able to blind a gate — and both arms must land. Healthy must reach
`established`; an armed `bus-drop` must reach `never-established` carrying a `bus_dial_failed`. The
second arm is the first one's negative control. What the lane proves and does not prove is written at
the top of the script.
