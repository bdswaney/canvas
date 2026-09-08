import { useEffect, useRef } from 'react';
import * as Y from 'yjs';
import type { Awareness } from 'y-protocols/awareness';
import { EditorState } from '@codemirror/state';
import { EditorView, keymap, lineNumbers, placeholder } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { yCollab } from 'y-codemirror.next';

const theme = EditorView.theme({
  '&': { fontSize: '14px', backgroundColor: 'transparent' },
  '&.cm-focused': { outline: 'none' },
  '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', padding: '12px 0' },
  '.cm-gutters': { backgroundColor: 'transparent', border: 'none', opacity: 0.4 },
  '.cm-scroller': { minHeight: '220px' },
});

/**
 * Editor binds a Y.Text to CodeMirror. yCollab handles the character-level
 * sync in both directions and draws every other client's caret and selection
 * using the color each of them publishes on the awareness channel.
 */
export function Editor({ text, awareness }: { text: Y.Text; awareness: Awareness }) {
  const host = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const element = host.current;
    if (!element) return;

    // Scope undo to this client's own edits, so undo never reverts a peer.
    const undoManager = new Y.UndoManager(text);
    const view = new EditorView({
      parent: element,
      state: EditorState.create({
        doc: text.toString(),
        extensions: [
          lineNumbers(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          placeholder('Start typing. Everyone in this room sees it as you type.'),
          EditorView.lineWrapping,
          theme,
          yCollab(text, awareness, { undoManager }),
        ],
      }),
    });

    return () => {
      view.destroy();
      undoManager.destroy();
    };
  }, [awareness, text]);

  return <div ref={host} />;
}
