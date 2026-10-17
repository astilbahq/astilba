// doBus conformance — the REAL wire codec + server fan-out + client, driven
// through the generic `runBusContract` over an in-memory socket pair. doBus passes the CORE block
// (establishment ordering, hello head lower bound, regId echo, frame fidelity through the codec,
// close-on-doubt); the loss/reorder GATED checks self-skip (doBus emits NO server `gap`
// — its loss detection is the reader's contiguity handling over real evictions, exercised in the
// integration lane). The FakeBus reference passes BOTH blocks (test/conformance/bus.test.ts).

import { makeDoBusHarness } from "./fixtures/do-bus";
import { runBusContract } from "./runner/bus";

runBusContract(makeDoBusHarness(), {
  label: "doBus (in-memory pair — real codec/server/client)",
});
