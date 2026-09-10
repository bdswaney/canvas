import { useEffect, useRef } from 'react';
import * as Y from 'yjs';
import type { Awareness } from 'y-protocols/awareness';
import { Compartment, EditorState } from '@codemirror/state';
import { EditorView, keymap, lineNumbers, placeholder } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { yCollab } from 'y-codemirror.next';
import { markdownSupport } from './markdown';

// Colors come from Mantine's CSS variables so the editor follows the app's
// color scheme. The dark flag is what tells CodeMirror which highlight style
// to apply and how to draw its own layers.
function editorTheme(dark: boolean) {
  return EditorView.theme(
    {
      '&': { fontSize: '14px', backgroundColor: 'transparent', color: 'var(--mantine-color-text)' },
      '&.cm-focused': { outline: 'none' },
      '.cm-content': {
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        padding: '12px 0',
        caretColor: 'var(--mantine-color-text)',
      },
      '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--mantine-color-text)' },
      '.cm-gutters': {
        backgroundColor: 'transparent',
        border: 'none',
        color: 'var(--mantine-color-dimmed)',
      },
      '.cm-placeholder': { color: 'var(--mantine-color-dimmed)' },
      '.cm-scroller': { minHeight: '220px' },
    },
    { dark },
  );
}

const themeCompartment = new Compartment();

/**
 * Editor binds a Y.Text to CodeMirror, editing Markdown. yCollab handles the
 * character-level sync in both directions and draws every other client's
 * caret and selection using the color each of them publishes on the awareness
 * channel.
 *
 * A parent that hides the editor should keep it mounted and pass hidden, not
 * unmount it: unmounting destroys the view and the undo history with it.
 */
export function Editor({
  text,
  awareness,
  dark,
  hidden = false,
}: {
  text: Y.Text;
  awareness: Awareness;
  dark: boolean;
  hidden?: boolean;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  // The scheme at mount seeds the initial theme without making the editor
  // effect depend on it; later changes go through the compartment below.
  const initialDark = useRef(dark);

  useEffect(() => {
    const element = host.current;
    if (!element) return;

    // Scope undo to this client's own edits, so undo never reverts a peer.
    const undoManager = new Y.UndoManager(text);
    const editor = new EditorView({
      parent: element,
      state: EditorState.create({
        doc: text.toString(),
        extensions: [
          lineNumbers(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          placeholder('Start typing Markdown. Everyone with this document open sees it as you type.'),
          EditorView.lineWrapping,
          markdownSupport(),
          themeCompartment.of(editorTheme(initialDark.current)),
          yCollab(text, awareness, { undoManager }),
        ],
      }),
    });
    view.current = editor;

    return () => {
      view.current = null;
      editor.destroy();
      undoManager.destroy();
    };
  }, [awareness, text]);

  // Swapping the theme through a compartment keeps the document, the
  // selection, and the peers' carets in place; rebuilding the view would not.
  useEffect(() => {
    view.current?.dispatch({ effects: themeCompartment.reconfigure(editorTheme(dark)) });
  }, [dark]);

  // A view under display: none measures as zero, so ask for a fresh
  // measurement once it is shown again rather than trusting stale geometry.
  useEffect(() => {
    if (!hidden) view.current?.requestMeasure();
  }, [hidden]);

  return <div ref={host} />;
}
