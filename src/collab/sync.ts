import { useEffect, useState } from 'react';
import * as Y from 'yjs';
import { Awareness } from 'y-protocols/awareness';
import { WebsocketProvider } from 'y-websocket';

// The relay lives behind the same origin as the app, so a tunnel that
// terminates TLS keeps working: derive the scheme from the page.
export function relayURL(location: Location = window.location): string {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${location.host}/api/sync/doc`;
}

export type Status = 'connecting' | 'connected' | 'disconnected';

// Close codes from sync.go. All are in the 4400-4499 range that y-websocket
// treats as permanent, so the client stops reconnecting rather than storming
// against a refusal it cannot fix.
const closeCodes = {
  signedOut: 4401,
  unknownDoc: 4404,
  notAMember: 4405,
} as const;

// What to tell someone whose socket was refused for good. Without this a
// refusal is indistinguishable from a network problem, and the app sits on
// "disconnected" forever with nothing to act on.
const refusalMessages: Record<number, string> = {
  [closeCodes.unknownDoc]: 'This document no longer exists.',
  [closeCodes.notAMember]:
    'You are not a member of the project this document belongs to. Someone may have removed you.',
};

export type Connection = {
  doc: Y.Doc;
  awareness: Awareness;
  docID: string;
  status: Status;
  synced: boolean;
  // peers counts everyone connected to this document.
  peers: number;
  // refused explains a close the client will never recover from, and is null
  // for an ordinary disconnect that will retry.
  refused: string | null;
};

/**
 * useSync owns one document and its relay connection for the component's
 * lifetime, and re-renders on connection, sync, and presence changes.
 */
export function useSync(docID: string, onSignedOut?: () => void): Connection {
  const [doc] = useState(() => new Y.Doc());
  // Awareness outlives any single provider, so presence state set by the UI
  // survives a reconnect and StrictMode's double mount.
  const [awareness] = useState(() => new Awareness(doc));
  const [status, setStatus] = useState<Status>('connecting');
  const [synced, setSynced] = useState(false);
  const [peers, setPeers] = useState(1);
  const [refused, setRefused] = useState<string | null>(null);

  // The provider is created here rather than in state so that StrictMode's
  // double mount tears its socket down and opens a fresh one, instead of
  // leaving the component holding a destroyed provider.
  useEffect(() => {
    const provider = new WebsocketProvider(relayURL(), docID, doc, { awareness });
    const onStatus = (event: { status: Status }) => {
      setStatus(event.status);
      // Remote awareness states outlive a dropped socket, so a count taken
      // while disconnected would claim company that is no longer reachable.
      if (event.status !== 'connected') setPeers(1);
    };
    const onSync = (isSynced: boolean) => setSynced(isSynced);
    const onAwareness = () => setPeers(awareness.getStates().size);

    // A close in the permanent range is the only notice the app gets that
    // the socket will not come back: a lapsed session is re-checked, and
    // anything else is explained to the person looking at the screen.
    const onClose = (event: CloseEvent | null) => {
      if (event === null) return;
      if (event.code === closeCodes.signedOut) {
        onSignedOut?.();
        return;
      }
      const message = refusalMessages[event.code];
      if (message) setRefused(message);
    };

    provider.on('status', onStatus);
    provider.on('sync', onSync);
    provider.on('connection-close', onClose);
    awareness.on('change', onAwareness);

    return () => {
      provider.off('status', onStatus);
      provider.off('sync', onSync);
      provider.off('connection-close', onClose);
      awareness.off('change', onAwareness);
      provider.destroy();
      setStatus('connecting');
      setSynced(false);
      setPeers(1);
      setRefused(null);
    };
  }, [awareness, doc, docID, onSignedOut]);

  return { doc, awareness, docID, status, synced, peers, refused };
}

// useSharedText hands out one named Y.Text for the document's lifetime.
export function useSharedText(doc: Y.Doc, name: string): Y.Text {
  const [text] = useState(() => doc.getText(name));
  return text;
}

/**
 * useTextSnapshot mirrors a Y.Text into React state for readers that want the
 * whole string, such as the preview. Updates are trailing-debounced: a
 * keystroke, or a burst of them from a peer, costs one re-render and one
 * parse rather than one per change.
 */
export function useTextSnapshot(text: Y.Text, delay = 150): string {
  const [snapshot, setSnapshot] = useState(() => text.toString());

  useEffect(() => {
    setSnapshot(text.toString());
    let timer: ReturnType<typeof setTimeout> | undefined;
    const observer = () => {
      if (timer !== undefined) return;
      timer = setTimeout(() => {
        timer = undefined;
        setSnapshot(text.toString());
      }, delay);
    };
    text.observe(observer);
    return () => {
      text.unobserve(observer);
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [delay, text]);

  return snapshot;
}
