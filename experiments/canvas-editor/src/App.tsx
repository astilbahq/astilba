import { useCallback, useRef, useState } from "react";
import { css } from "../styled-system/css";
import { Canvas, type CanvasHandle } from "./Canvas";
import {
  type Binding,
  bindingId,
  type Configuration,
  configurationProblems,
  connectionProblem,
  type Fixture,
  kindLabel,
  type Service,
  type ServiceKind,
} from "./domain";
import { Properties } from "./Properties";
import { loadFixture, saveFixture } from "./storage";
import {
  button,
  error,
  input,
  listButton,
  muted,
  primary,
  row,
} from "./styles";

export function App() {
  const [fixture, setFixture] = useState<Fixture>(() =>
    location.hash === "#large" ? "large" : "starter",
  );
  return (
    <Editor
      key={fixture}
      fixture={fixture}
      onFixture={(value) => {
        location.hash = value;
        setFixture(value);
      }}
    />
  );
}

function Editor({
  fixture,
  onFixture,
}: {
  fixture: Fixture;
  onFixture: (fixture: Fixture) => void;
}) {
  const [loaded] = useState(() => loadFixture(fixture));
  const [config, setConfig] = useState<Configuration>(loaded.config);
  const [selected, setSelected] = useState(loaded.config.services[0].id);
  const [view, setView] = useState<"canvas" | "list">("canvas");
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [draftDirty, setDraftDirty] = useState(false);
  const [showErrors, setShowErrors] = useState(false);
  const [message, setMessage] = useState("");
  const [failure, setFailure] = useState(false);
  const [search, setSearch] = useState("");
  const canvas = useRef<CanvasHandle>(null);
  const addMenu = useRef<HTMLDetailsElement>(null);
  const service =
    config.services.find((item) => item.id === selected) ?? config.services[0];
  const notify = useCallback((text: string, isError = false) => {
    setMessage(text);
    setFailure(isError);
  }, []);
  const dirtyLayout = useCallback(() => setLayoutDirty(true), []);
  const changeService = useCallback((updated: Service) => {
    setConfig((previous) => ({
      ...previous,
      services: previous.services.map((item) =>
        item.id === updated.id ? updated : item,
      ),
    }));
    setDraftDirty(true);
  }, []);
  const connect = useCallback(
    (binding: Binding) => {
      const problem = connectionProblem(config, binding);
      if (problem) {
        notify(problem, true);
        return;
      }
      setConfig({ ...config, bindings: [...config.bindings, binding] });
      setDraftDirty(true);
      notify("Connection added to the draft.");
    },
    [config, notify],
  );
  const remove = useCallback(
    (binding: Binding) => {
      setConfig((previous) => ({
        ...previous,
        bindings: previous.bindings.filter(
          (item) => bindingId(item) !== bindingId(binding),
        ),
      }));
      setDraftDirty(true);
      notify("Connection removed from the draft.");
    },
    [notify],
  );
  const showOnCanvas = useCallback(() => {
    setView("canvas");
    requestAnimationFrame(() => canvas.current?.focus(selected));
  }, [selected]);
  function addService(kind: ServiceKind) {
    if (config.services.length >= 250) return;
    const id = `svc-${crypto.randomUUID()}`;
    let number = 1;
    while (
      config.services.some(
        (item) =>
          item.name.toLowerCase() ===
          `${kindLabel(kind)} ${number}`.toLowerCase(),
      )
    )
      number++;
    const base = { id, name: `${kindLabel(kind)} ${number}` };
    const created: Service =
      kind === "volume"
        ? { ...base, kind, sizeGiB: 10 }
        : {
            ...base,
            kind,
            image:
              kind === "database" ? "postgres:18" : "ghcr.io/example/app:1.0",
            port: kind === "database" ? 5432 : 3000,
          };
    setConfig({ ...config, services: [...config.services, created] });
    setSelected(id);
    setDraftDirty(true);
    setLayoutDirty(true);
    if (addMenu.current) addMenu.current.open = false;
    notify(`${created.name} added to the draft.`);
  }
  function saveDraft() {
    setShowErrors(true);
    const problems = configurationProblems(config);
    if (problems.length) {
      notify(`Draft not saved. ${problems[0]}`, true);
      return;
    }
    try {
      saveFixture(fixture, "draft", config);
      setDraftDirty(false);
      notify("Draft saved in this browser.");
    } catch {
      notify(
        "Draft could not be saved. Browser storage may be unavailable or full.",
        true,
      );
    }
  }
  function saveLayout() {
    if (!canvas.current) return;
    try {
      saveFixture(fixture, "layout", canvas.current.layout());
      setLayoutDirty(false);
      notify(
        "Layout saved in this browser. Draft configuration was not saved.",
      );
    } catch {
      notify(
        "Layout could not be saved. Browser storage may be unavailable or full.",
        true,
      );
    }
  }
  const filtered = config.services.filter((item) =>
    item.name.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <div>
      <header
        className={css({
          bg: "paper",
          borderBottom: "1px solid",
          borderColor: "line",
          px: { base: "4", md: "6" },
          py: "4",
        })}
      >
        <div
          className={css({
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: "4",
            flexWrap: "wrap",
          })}
        >
          <div className={row}>
            <span
              aria-hidden="true"
              className={css({
                display: "grid",
                placeItems: "center",
                width: "32px",
                height: "32px",
                bg: "accent",
                color: "white",
                rounded: "lg",
                fontSize: "20px",
              })}
            >
              a
            </span>
            <div>
              <h1
                className={css({
                  fontSize: "20px",
                  fontWeight: "600",
                  lineHeight: "1.2",
                })}
              >
                Astilba{" "}
                <span className={css({ color: "muted", fontWeight: "400" })}>
                  / Canvas lab
                </span>
              </h1>
              <p className={muted}>Shape a workspace on your own terms.</p>
            </div>
          </div>
          <label className={row}>
            Workspace
            <select
              aria-label="Workspace fixture"
              className={input}
              style={{ width: "auto" }}
              value={fixture}
              onChange={(event) => onFixture(event.target.value as Fixture)}
            >
              <option value="starter">Starter · 4 services</option>
              <option value="large">Stress test · 250 services</option>
            </select>
          </label>
        </div>
        <div
          className={css({
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            gap: "3",
            flexWrap: "wrap",
            mt: "5",
          })}
        >
          <div className={row}>
            <fieldset aria-label="Workspace view" className={row}>
              <button
                className={button}
                type="button"
                aria-pressed={view === "canvas"}
                onClick={() => setView("canvas")}
              >
                Canvas
              </button>
              <button
                className={button}
                type="button"
                aria-pressed={view === "list"}
                onClick={() => setView("list")}
              >
                Services <span className={muted}>{config.services.length}</span>
              </button>
            </fieldset>
            <details ref={addMenu} className={css({ position: "relative" })}>
              <summary className={button}>+ Add service</summary>
              <div
                className={css({
                  position: "absolute",
                  top: "calc(100% + 8px)",
                  left: "0",
                  zIndex: "20",
                  bg: "paper",
                  border: "1px solid",
                  borderColor: "control",
                  rounded: "md",
                  p: "2",
                  display: "flex",
                  flexDirection: "column",
                  gap: "1",
                  minWidth: "180px",
                })}
              >
                {(["application", "database", "volume"] as const).map(
                  (kind) => (
                    <button
                      key={kind}
                      type="button"
                      className={button}
                      disabled={config.services.length >= 250}
                      onClick={() => addService(kind)}
                    >
                      {kindLabel(kind)}
                    </button>
                  ),
                )}
              </div>
            </details>
          </div>
          <div className={row}>
            <span className={muted}>
              {layoutDirty ? "Layout unsaved" : "Layout ready"} ·{" "}
              {draftDirty ? "Draft unsaved" : "Draft ready"}
            </span>
            <button className={button} type="button" onClick={saveLayout}>
              Save layout
            </button>
            <button className={primary} type="button" onClick={saveDraft}>
              Save draft
            </button>
          </div>
        </div>
      </header>
      <div
        className={css({
          px: { base: "4", md: "6" },
          py: "2",
          bg: "paper",
          borderBottom: "1px solid",
          borderColor: "line",
          minHeight: "38px",
          display: "flex",
          alignItems: "center",
          gap: "3",
          flexWrap: "wrap",
        })}
      >
        <span className={muted}>
          Prototype · browser-local drafts · nothing is deployed
        </span>
        <p role="status" className={failure ? error : muted}>
          {message}
        </p>
      </div>
      {loaded.warnings.length > 0 && (
        <div role="alert" className={css({ p: "4", bg: "tint", color: "ink" })}>
          {loaded.warnings.map((warning) => (
            <p key={warning}>{warning}</p>
          ))}
        </div>
      )}
      <main
        className={css({
          display: "grid",
          gridTemplateColumns: {
            base: "minmax(0, 1fr)",
            md: "minmax(0, 1fr) 320px",
          },
          height: { md: "calc(100dvh - 177px)" },
          minHeight: { md: "560px" },
        })}
      >
        <section
          aria-label="Workspace editor"
          className={css({
            position: "relative",
            minWidth: "0",
            minHeight: "560px",
          })}
        >
          <div
            hidden={view !== "canvas"}
            className={css({
              width: "100%",
              height: "100%",
              minHeight: "560px",
              position: "absolute",
              inset: "0",
            })}
          >
            <Canvas
              ref={canvas}
              config={config}
              initialLayout={loaded.layout}
              hasLayout={loaded.hasLayout}
              selected={selected}
              onSelect={setSelected}
              onConnect={connect}
              onDirty={dirtyLayout}
            />
            <p
              className={css({
                position: "absolute",
                top: "4",
                left: "4",
                right: "4",
                width: "fit-content",
                maxWidth: "calc(100% - 32px)",
                bg: "paper",
                border: "1px solid",
                borderColor: "line",
                rounded: "md",
                px: "3",
                py: "2",
                fontSize: "12px",
                color: "muted",
                pointerEvents: "none",
              })}
            >
              Connect from right to left. Select a service, then use arrow keys
              to move it.
            </p>
          </div>
          {view === "list" && (
            <section
              aria-label="Service list"
              className={css({
                p: { base: "4", md: "6" },
                height: "100%",
                overflowY: "auto",
              })}
            >
              <h2
                className={css({
                  fontSize: "20px",
                  fontWeight: "600",
                  mb: "2",
                })}
              >
                Your services
              </h2>
              <p className={muted}>
                Select a service to edit its properties and connections.
              </p>
              <label className={css({ display: "block", mt: "5", mb: "4" })}>
                Search services
                <input
                  type="search"
                  className={input}
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </label>
              <ul
                className={css({
                  display: "grid",
                  gap: "3",
                  gridTemplateColumns: {
                    base: "1fr",
                    lg: "repeat(2, minmax(0, 1fr))",
                  },
                })}
              >
                {filtered.map((item) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      aria-label={`Edit ${item.name}`}
                      aria-pressed={item.id === selected}
                      onClick={() => setSelected(item.id)}
                      className={listButton}
                    >
                      <span className={muted}>{kindLabel(item.kind)}</span>
                      <span
                        className={css({
                          display: "block",
                          fontSize: "16px",
                          mt: "1",
                        })}
                      >
                        {item.name || "Unnamed service"}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
              {!filtered.length && (
                <p className={muted}>No services match your search.</p>
              )}
            </section>
          )}
        </section>
        <Properties
          key={service.id}
          service={service}
          config={config}
          showErrors={showErrors}
          onChange={changeService}
          onConnect={connect}
          onRemove={remove}
          onFocus={showOnCanvas}
        />
      </main>
    </div>
  );
}
