import { useEffect, useState, type RefObject } from 'react';
import { removeAwarenessStates, type Awareness } from 'y-protocols/awareness';

// Peers are told apart by color first and name second, so the palette is
// spread around the hue circle and kept dark enough for white label text.
const palette = [
  { color: '#e8590c', colorLight: '#ffe8cc' },
  { color: '#c2255c', colorLight: '#ffdeeb' },
  { color: '#7048e8', colorLight: '#e5dbff' },
  { color: '#1c7ed6', colorLight: '#d0ebff' },
  { color: '#0ca678', colorLight: '#c3fae8' },
  { color: '#5c940d', colorLight: '#e9fac8' },
  { color: '#f08c00', colorLight: '#fff3bf' },
  { color: '#495057', colorLight: '#e9ecef' },
];

const adjectives = ['Amber', 'Brisk', 'Candid', 'Dapper', 'Eager', 'Fluent', 'Gentle', 'Humble'];
const animals = ['Otter', 'Heron', 'Lynx', 'Marten', 'Puffin', 'Raven', 'Seal', 'Tapir'];

export type User = { name: string; color: string; colorLight: string };

// Cursor coordinates are fractions of the shared surface, not pixels, so a
// pointer lands in the same place on a differently sized window.
export type Cursor = { x: number; y: number };

export type Peer = { clientID: number; user: User; cursor: Cursor | null };

const identityKey = 'canvas.identity';

function randomIdentity(): User {
  const swatch = palette[Math.floor(Math.random() * palette.length)];
  const name = `${adjectives[Math.floor(Math.random() * adjectives.length)]} ${
    animals[Math.floor(Math.random() * animals.length)]
  }`;
  return { name, ...swatch };
}

// identity is remembered per browser so a reload keeps the same name and
// color for everyone watching.
export function loadIdentity(): User {
  try {
    const stored = localStorage.getItem(identityKey);
    if (stored) {
      const user = JSON.parse(stored) as Partial<User>;
      if (user.name && user.color && user.colorLight) return user as User;
    }
  } catch {
    // Private windows and blocked storage fall back to a fresh identity.
  }
  const user = randomIdentity();
  try {
    localStorage.setItem(identityKey, JSON.stringify(user));
  } catch {
    // Not being able to remember the identity is not worth failing over.
  }
  return user;
}

function readPeers(awareness: Awareness): Peer[] {
  const peers: Peer[] = [];
  awareness.getStates().forEach((state, clientID) => {
    if (clientID === awareness.clientID) return;
    const user = state.user as User | undefined;
    if (!user?.name) return;
    peers.push({ clientID, user, cursor: (state.cursor as Cursor | undefined) ?? null });
  });
  // A stable order keeps React from reshuffling the cursor nodes.
  return peers.sort((a, b) => a.clientID - b.clientID);
}

/**
 * usePresence publishes this client's identity and pointer position on the
 * awareness channel and returns the other clients in the room.
 */
export function usePresence(awareness: Awareness, surface: RefObject<HTMLElement | null>): Peer[] {
  const [user] = useState(loadIdentity);
  const [peers, setPeers] = useState<Peer[]>([]);

  useEffect(() => {
    awareness.setLocalStateField('user', user);
    const onChange = () => setPeers(readPeers(awareness));
    awareness.on('change', onChange);
    onChange();
    return () => awareness.off('change', onChange);
  }, [awareness, user]);

  useEffect(() => {
    let pending: Cursor | null = null;
    let frame = 0;
    let published = false;

    // Pointer events fire far faster than the screen refreshes, and every
    // published position is a network message: coalesce them per frame.
    const flush = () => {
      frame = 0;
      awareness.setLocalStateField('cursor', pending);
      published = pending !== null;
    };
    const schedule = (cursor: Cursor | null) => {
      pending = cursor;
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
      // Leave no stale cursor behind for the peers still in the room.
      if (published) awareness.setLocalStateField('cursor', null);
    };
  }, [awareness, surface]);

  return peers;
}
