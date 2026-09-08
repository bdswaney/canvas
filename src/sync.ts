import { useEffect, useState } from 'react';
import * as Y from 'yjs';
import { Awareness } from 'y-protocols/awareness';
import { WebsocketProvider } from 'y-websocket';

// The relay lives behind the same origin as the app, so a tunnel that
// terminates TLS keeps working: derive the scheme from the page.
export function relayURL(location: Location = window.location): string {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${location.host}/api/sync`;
}

// Rooms are named by the first path segment, so /notes and /sketches are
// separate documents and a bare / is the default room.
export function roomName(pathname: string = window.location.pathname): string {
  const segment = pathname.split('/').filter(Boolean)[0] ?? 'default';
  return /^[A-Za-z0-9._-]{1,64}$/.test(segment) ? segment : 'default';
}

export type Status = 'connecting' | 'connected' | 'disconnected';

export type Connection = {
  doc: Y.Doc;
  awareness: Awareness;
  room: string;
  status: Status;
  synced: boolean;
  peers: number;
};

// useSync owns one document and its relay connection for the component's
// lifetime, and re-renders on connection, sync, and presence changes.
export function useSync(room: string = roomName()): Connection {
  const [doc] = useState(() => new Y.Doc());
  // Awareness outlives any single provider, so presence state set by the UI
  // survives a reconnect and StrictMode's double mount.
  const [awareness] = useState(() => new Awareness(doc));
  const [status, setStatus] = useState<Status>('connecting');
  const [synced, setSynced] = useState(false);
  const [peers, setPeers] = useState(1);

  // The provider is created here rather than in state so that StrictMode's
  // double mount tears its socket down and opens a fresh one, instead of
  // leaving the component holding a destroyed provider.
  useEffect(() => {
    const provider = new WebsocketProvider(relayURL(), room, doc, { awareness });
    const onStatus = (event: { status: Status }) => setStatus(event.status);
    const onSync = (isSynced: boolean) => setSynced(isSynced);
    const onAwareness = () => setPeers(awareness.getStates().size);

    provider.on('status', onStatus);
    provider.on('sync', onSync);
    awareness.on('change', onAwareness);

    return () => {
      provider.off('status', onStatus);
      provider.off('sync', onSync);
      awareness.off('change', onAwareness);
      provider.destroy();
      setStatus('connecting');
      setSynced(false);
      setPeers(1);
    };
  }, [awareness, doc, room]);

  return { doc, awareness, room, status, synced, peers };
}

// useSharedText mirrors a Y.Text into React state and writes edits back.
export function useSharedText(doc: Y.Doc, name: string) {
  const [text] = useState(() => doc.getText(name));
  const [value, setValue] = useState(() => text.toString());

  useEffect(() => {
    const observer = () => setValue(text.toString());
    text.observe(observer);
    observer();
    return () => text.unobserve(observer);
  }, [text]);

  // Replace the whole contents in one transaction. Character-level diffing
  // arrives with the editor; this keeps the demo honest about being a
  // last-write-wins textarea.
  const replace = (next: string) => {
    doc.transact(() => {
      text.delete(0, text.length);
      text.insert(0, next);
    });
  };

  return [value, replace] as const;
}
