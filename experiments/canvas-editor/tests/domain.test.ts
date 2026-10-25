import assert from "node:assert/strict";
import { test } from "node:test";
import {
  type Binding,
  connectionProblem,
  fixtureData,
  parseConfiguration,
  parseLayout,
} from "../src/domain.ts";

void test("typed connections reject wrong directions, missing services and competing bindings", () => {
  const { config } = fixtureData("starter");
  const base = { ...config, bindings: [] };
  const database: Binding = {
    source: "svc-web-0",
    target: "svc-database-0",
    kind: "database",
  };
  const storage: Binding = {
    source: "svc-database-0",
    target: "svc-volume-0",
    kind: "storage",
  };
  assert.equal(connectionProblem(base, database), null);
  assert.equal(connectionProblem(base, storage), null);
  for (const invalid of [
    { ...database, source: database.target, target: database.source },
    { ...database, target: database.source },
    { ...database, target: "svc-missing" },
    { ...storage, source: "svc-volume-0" },
    { ...storage, target: "svc-web-0" },
  ])
    assert.ok(connectionProblem(base, invalid));
  assert.ok(connectionProblem(config, database));
  const anotherDatabase = {
    ...config.services[2],
    id: "svc-other",
    name: "Other database",
  };
  assert.ok(
    connectionProblem(
      { ...config, services: [...config.services, anotherDatabase] },
      { ...database, target: anotherDatabase.id },
    ),
  );
});

void test("persisted input validates identities, bindings and numeric bounds before it enters the editor", () => {
  const { config, layout } = fixtureData("starter");
  assert.deepEqual(parseConfiguration(JSON.stringify(config)), config);
  assert.deepEqual(parseLayout(JSON.stringify(layout)), layout);
  for (const mutate of [
    (value: typeof config) => {
      value.services[1].id = value.services[0].id;
    },
    (value: typeof config) => {
      value.services[1].name = " web ";
    },
    (value: typeof config) => {
      value.bindings[0].target = "svc-missing";
    },
    (value: typeof config) => {
      value.bindings.push(value.bindings[0]);
    },
    (value: typeof config) => {
      if (value.services[0].kind !== "volume") value.services[0].port = 0;
    },
  ]) {
    const invalid = structuredClone(config);
    mutate(invalid);
    assert.throws(() => parseConfiguration(JSON.stringify(invalid)));
  }
  for (const invalid of [
    {
      ...config,
      services: config.services.map((service, index) =>
        index === 1 ? { ...service, kind: ["application"] } : service,
      ),
    },
    {
      ...config,
      bindings: [
        { source: "svc-volume-0", target: "svc-web-0", kind: ["database"] },
      ],
    },
  ])
    assert.throws(() => parseConfiguration(JSON.stringify(invalid)));
  for (const invalid of [
    { ...layout, version: 2 },
    { ...layout, positions: { "svc-web-0": { x: 1_000_001, y: 0 } } },
    { ...layout, viewport: { x: 0, y: 0, zoom: 0 } },
    {
      ...layout,
      positions: { __proto__: { x: 0, y: 0 } },
      viewport: { x: 0, y: null, zoom: 1 },
    },
  ])
    assert.throws(() => parseLayout(JSON.stringify(invalid)));
  assert.throws(() => parseConfiguration(" ".repeat(500_001)));
});
