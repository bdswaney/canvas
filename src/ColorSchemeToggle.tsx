import { ActionIcon, useComputedColorScheme, useMantineColorScheme } from '@mantine/core';

// Two 16px glyphs rather than an icon dependency: a sun for the light scheme
// the click will switch to, a moon for the dark one.
function SunIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round">
      <path d="M20 14.5A8.5 8.5 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5Z" />
    </svg>
  );
}

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
      variant="default"
      size="lg"
      onClick={() => setColorScheme(next)}
      aria-label={`Switch to the ${next} color scheme`}
      title={`Switch to the ${next} color scheme`}
    >
      {scheme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </ActionIcon>
  );
}
