import { IconMoon, IconSun } from '@tabler/icons-react';
import { ActionIcon, useComputedColorScheme, useMantineColorScheme } from '@mantine/core';

/**
 * The scheme starts as "auto" and follows the system. Toggling picks a
 * scheme explicitly; Mantine remembers the choice across reloads.
 */
export function ColorSchemeToggle() {
  const { setColorScheme } = useMantineColorScheme();
  const scheme = useComputedColorScheme('light');
  const next = scheme === 'dark' ? 'light' : 'dark';

  return (
    <ActionIcon
      variant="subtle"
      color="gray"
      size="lg"
      onClick={() => setColorScheme(next)}
      aria-label={`Switch to the ${next} color scheme`}
      title={`Switch to the ${next} color scheme`}
    >
      {/* The glyph shows the scheme the click will switch to. */}
      {scheme === 'dark' ? <IconSun size={18} stroke={1.5} /> : <IconMoon size={18} stroke={1.5} />}
    </ActionIcon>
  );
}
