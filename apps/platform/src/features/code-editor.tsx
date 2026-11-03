import "./code-editor.css";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { Compartment, EditorState } from "@codemirror/state";
import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { useContext, useEffect, useRef } from "react";
import { ThemeContext } from "../theme";

export function CodeEditor({
  value,
  change,
  label,
  readOnly,
  line,
}: {
  value: string;
  change: (text: string) => void;
  label: string;
  readOnly: boolean;
  line?: { line: number };
}) {
  const { theme } = useContext(ThemeContext);
  const host = useRef<HTMLDivElement>(null),
    view = useRef<EditorView>(null),
    latest = useRef({ change, value, theme });
  const appearance = useRef(new Compartment());
  latest.current = { change, value, theme };
  useEffect(() => {
    if (!host.current) return;
    const editor = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: latest.current.value,
        extensions: [
          yaml(),
          history(),
          lineNumbers(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          EditorView.lineWrapping,
          EditorView.cspNonce.of(
            document.querySelector<HTMLMetaElement>('meta[name="style-nonce"]')
              ?.content ?? "",
          ),
          EditorState.readOnly.of(readOnly),
          EditorView.editable.of(!readOnly),
          EditorView.contentAttributes.of({
            "aria-label": label,
            ...(readOnly ? { tabindex: "0" } : {}),
          }),
          appearance.current.of(
            EditorView.theme({}, { dark: latest.current.theme === "dark" }),
          ),
          EditorView.updateListener.of((update) => {
            if (update.docChanged)
              latest.current.change(update.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = editor;
    return () => {
      view.current = null;
      editor.destroy();
    };
  }, [label, readOnly]);
  useEffect(() => {
    const editor = view.current;
    if (editor && editor.state.doc.toString() !== value)
      editor.dispatch({
        changes: { from: 0, to: editor.state.doc.length, insert: value },
      });
  }, [value]);
  useEffect(() => {
    view.current?.dispatch({
      effects: appearance.current.reconfigure(
        EditorView.theme({}, { dark: theme === "dark" }),
      ),
    });
  }, [theme]);
  useEffect(() => {
    const editor = view.current;
    if (editor && line) {
      const at = editor.state.doc.line(
        Math.max(1, Math.min(line.line, editor.state.doc.lines)),
      ).from;
      editor.dispatch({
        selection: { anchor: at },
        effects: EditorView.scrollIntoView(at),
      });
      editor.focus();
    }
  }, [line]);
  return <div ref={host} className="compose-code-editor" />;
}
