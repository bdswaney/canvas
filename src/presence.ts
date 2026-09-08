import { useEffect, useMemo, useState, type RefObject } from 'react';
import { removeAwarenessStates, type Awareness } from 'y-protocols/awareness';

// Peers are told apart by color first and name second, so the palette is
// spread around the hue circle. Every entry is a mid tone: dark enough to
// carry white label text, light enough to stay visible on a dark background.
const palette = [
  '#f76707',
  '#e64980',
  '#ae3ec9',
  '#7048e8',
  '#1c7ed6',
  '#0c8599',
  '#099268',
  '#66a80f',
];

// Selections are drawn in a translucent tint of the peer's color rather than
// a pastel, so a peer's highlight reads on whichever scheme the viewer uses.
function tint(color: string): string {
  return `${color}55`;
}

export type User = { name: string; color: string; colorLight: string };

// Pointer coordinates are fractions of the shared surface, not pixels, so a
// pointer lands in the same place on a differently sized window. The field is
// named "pointer" because y-codemirror.next owns "cursor" for text selections.
export type Pointer = { x: number; y: number };

export type Peer = { clientID: number; user: User; pointer: Pointer | null };

// A person's color is derived from their name rather than stored, so they
// look the same to everyone, on every device, with nothing to keep in sync.
function paletteIndex(username: string): number {
  let hash = 0;
  for (let i = 0; i < username.length; i += 1) {
    hash = (hash * 31 + username.charCodeAt(i)) | 0;
  }
  return Math.abs(hash) % palette.length;
}

export function identityFor(username: string): User {
  const color = palette[paletteIndex(username)];
  return { name: username, color, colorLight: tint(color) };
}

function readPeers(awareness: Awareness): Peer[] {
  const peers: Peer[] = [];
  awareness.getStates().forEach((state, clientID) => {
    if (clientID === awareness.clientID) return;
    const user = state.user as User | undefined;
    if (!user?.name) return;
    peers.push({ clientID, user, pointer: (state.pointer as Pointer | undefined) ?? null });
  });
  // A stable order keeps React from reshuffling the cursor nodes.
  return peers.sort((a, b) => a.clientID - b.clientID);
}

/**
 * usePresence publishes this client's identity and pointer position on the
 * awareness channel and returns the other clients in the room.
 */
export function usePresence(
  awareness: Awareness,
  surface: RefObject<HTMLElement | null>,
  username: string,
): Peer[] {
  const user = useMemo(() => identityFor(username), [username]);
  const [peers, setPeers] = useState<Peer[]>([]);

  useEffect(() => {
    awareness.setLocalStateField('user', user);
    const onChange = () => setPeers(readPeers(awareness));
    awareness.on('change', onChange);
    onChange();
    return () => awareness.off('change', onChange);
  }, [awareness, user]);

  useEffect(() => {
    let pending: Pointer | null = null;
    let frame = 0;
    let published = false;

    // Pointer events fire far faster than the screen refreshes, and every
    // published position is a network message: coalesce them per frame.
    const flush = () => {
      frame = 0;
      awareness.setLocalStateField('pointer', pending);
      published = pending !== null;
    };
    const schedule = (pointer: Pointer | null) => {
      pending = pointer;
      if (frame === 0) frame = requestAnimationFrame(flush);
    };

    const onPointerMove = (event: PointerEvent) => {
      const element = surface.current;
      if (!element) return;
      const rect = element.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) return;
      const x = (event.clientX - rect.left) / rect.width;
      const y = (event.clientY - rect.top) / rect.height;
      // Outside the shared surface the pointer is nobody else's business.
      schedule(x < 0 || x > 1 || y < 0 || y > 1 ? null : { x, y });
    };
    const onHide = () => schedule(null);
    // y-websocket only announces departures from Node, so a closing tab
    // would otherwise linger in the room until awareness times it out.
    const onUnload = () => removeAwarenessStates(awareness, [awareness.clientID], 'page closed');

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerleave', onHide);
    window.addEventListener('blur', onHide);
    document.addEventListener('visibilitychange', onHide);
    window.addEventListener('pagehide', onUnload);

    return () => {
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerleave', onHide);
      window.removeEventListener('blur', onHide);
      document.removeEventListener('visibilitychange', onHide);
      window.removeEventListener('pagehide', onUnload);
      if (frame !== 0) cancelAnimationFrame(frame);
      // Leave no stale pointer behind for the peers still in the room.
      if (published) awareness.setLocalStateField('pointer', null);
    };
  }, [awareness, surface]);

  return peers;
}
