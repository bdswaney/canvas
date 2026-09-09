import type { Peer } from './presence';

// The overlay covers the shared surface and never takes the pointer, so the
// cursors are decoration over a fully usable page.
const overlayStyle: React.CSSProperties = {
  position: 'absolute',
  inset: 0,
  overflow: 'hidden',
  pointerEvents: 'none',
  zIndex: 200,
};

const labelStyle: React.CSSProperties = {
  position: 'absolute',
  left: 14,
  top: 16,
  padding: '2px 6px',
  borderRadius: 4,
  fontSize: 11,
  fontWeight: 600,
  lineHeight: 1.4,
  color: 'white',
  whiteSpace: 'nowrap',
};

export function Cursors({ peers }: { peers: Peer[] }) {
  return (
    <div style={overlayStyle} aria-hidden>
      {peers.map((peer) =>
        peer.pointer === null ? null : (
          <div
            key={peer.clientID}
            style={{
              position: 'absolute',
              left: `${peer.pointer.x * 100}%`,
              top: `${peer.pointer.y * 100}%`,
              // Pointers move in discrete awareness updates; a short
              // transition reads as motion rather than teleporting.
              transition: 'left 80ms linear, top 80ms linear',
            }}
          >
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
              <path
                d="M1 1L6.5 14.5L8.6 8.6L14.5 6.5L1 1Z"
                fill={peer.user.color}
                stroke="white"
                strokeWidth="1.2"
                strokeLinejoin="round"
              />
            </svg>
            <div style={{ ...labelStyle, backgroundColor: peer.user.color }}>{peer.user.name}</div>
          </div>
        ),
      )}
    </div>
  );
}
