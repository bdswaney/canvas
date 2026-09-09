import { useEffect, useState } from 'react';
import { Alert, Button, Stack } from '@mantine/core';

/**
 * Archive hides something. It is deliberately not called Delete: nothing is
 * removed, and a document's saved versions outlive it.
 *
 * The confirmation is a second click rather than a dialog, which is
 * proportionate for something the database can undo. It disarms itself after
 * a few seconds, so an armed button is never left sitting under the pointer
 * waiting for an unrelated click — there is no un-archive in the app yet, so
 * an accidental second click cannot be taken back from here.
 */
export function Archive({
  what,
  archive,
  done,
}: {
  what: string;
  archive: () => Promise<void>;
  done: () => Promise<void>;
}) {
  const [asked, setAsked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!asked) return;
    const timer = setTimeout(() => setAsked(false), 4000);
    return () => clearTimeout(timer);
  }, [asked]);

  const run = async () => {
    if (!asked) {
      setAsked(true);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await archive();
      await done();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : `Could not archive the ${what}`);
    } finally {
      setBusy(false);
      setAsked(false);
    }
  };

  return (
    <Stack gap={4}>
      <Button
        size="compact-sm"
        variant="subtle"
        color={asked ? 'red' : 'gray'}
        loading={busy}
        onClick={() => void run()}
      >
        {asked ? `Archive this ${what}?` : 'Archive'}
      </Button>
      {error && (
        <Alert color="red" variant="light" p="xs">
          {error}
        </Alert>
      )}
    </Stack>
  );
}
