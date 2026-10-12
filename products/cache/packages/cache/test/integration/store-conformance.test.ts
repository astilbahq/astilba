











import { runStoreContract } from "../conformance/runner/store";
import { makeCloudflareKvStore } from "./store-harness";

runStoreContract(makeCloudflareKvStore, {
  label: "cloudflareKV (real KV namespace binding)",
  ttlFloorSeconds: 60,
});
