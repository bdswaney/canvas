import { useCallback, useState } from 'react';
import { IconUserMinus, IconUserPlus, IconUsersGroup } from '@tabler/icons-react';
import { Alert, Badge, Button, Group, Loader, Select, Stack, Text, Title } from '@mantine/core';
import {
  addMember,
  listMembers,
  listUsers,
  removeMember,
  useRefetched,
  type Member,
} from './projects';

/**
 * Members manages who can reach a project.
 *
 * Membership is the whole authorization boundary: a document is reachable
 * only by members of its project. There are no roles
 * — any member can add or remove any other — and the last member cannot
 * leave, because a project with nobody in it is unreachable by anyone who
 * could put it right.
 */
export function Members({ projectID, me }: { projectID: string; me: string }) {
  // me is the signed-in username. The session endpoint reports a name, not an
  // id, and this is only used to label a row and word a button — the server
  // decides what anyone may actually do.
  const members = useRefetched(useCallback(() => listMembers(projectID), [projectID]));
  const everyone = useRefetched(useCallback(() => listUsers(), []));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const change = async (action: Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action;
      await members.refresh();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not change the members');
    } finally {
      setBusy(false);
    }
  };

  const current = members.value ?? [];
  const joined = new Set(current.map((member) => member.userId));
  const addable = (everyone.value ?? []).filter((person) => !joined.has(person.userId));
  const last = current.length <= 1;

  return (
    <Stack>
      <Title order={2}>
        <Group gap="xs">
          <IconUsersGroup size={22} stroke={1.5} />
          Members
        </Group>
      </Title>
      <Text size="sm" c="dimmed">
        Everyone here can open every document in this project, and can add or remove anyone
        else.
      </Text>

      {(error ?? members.error) && (
        <Alert color="red" variant="light">
          {error ?? members.error}
        </Alert>
      )}

      <Select
        label="Add someone"
        placeholder={addable.length === 0 ? 'Everyone is already a member' : 'Pick a person'}
        disabled={addable.length === 0 || busy}
        data={addable.map((person) => ({ value: person.userId, label: person.username }))}
        leftSection={<IconUserPlus size={16} stroke={1.5} />}
        searchable
        value={null}
        onChange={(userID) => {
          if (userID) void change(addMember(projectID, userID));
        }}
      />

      {members.value === null ? (
        <Loader />
      ) : (
        <Stack gap="xs">
          {current.map((member) => (
            <Group key={member.userId} justify="space-between">
              <Group gap="xs">
                <Text>{member.username || member.userId}</Text>
                {member.username === me && (
                  <Badge size="sm" variant="light">
                    you
                  </Badge>
                )}
              </Group>
              <Button
                size="compact-sm"
                variant="subtle"
                color="gray"
                loading={busy}
                // The last member cannot leave; the server refuses it too.
                disabled={last}
                leftSection={<IconUserMinus size={15} stroke={1.5} />}
                onClick={() => void change(removeMember(projectID, member.userId))}
              >
                {member.username === me ? 'Leave' : 'Remove'}
              </Button>
            </Group>
          ))}
        </Stack>
      )}
    </Stack>
  );
}

export type { Member };
