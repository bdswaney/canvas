import { Anchor, type AnchorProps } from '@mantine/core';
import { navigate } from '../app/routes';

/**
 * Link is an in-app anchor: a real href, so it can be middle-clicked, opened
 * in a new tab, focused, and read as a link, but handled by the client-side
 * router on an ordinary click.
 */
export function Link({
  to,
  children,
  ...props
}: AnchorProps & { to: string; children: React.ReactNode }) {
  return (
    <Anchor
      {...props}
      href={to}
      onClick={(event) => {
        // Leave the modified clicks to the browser: those are deliberate
        // requests for a new tab or window.
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
        event.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </Anchor>
  );
}
