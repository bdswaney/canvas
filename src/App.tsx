import { AppShell, Badge, Container, Group, Paper, Stack, Text, Title } from '@mantine/core';

export function App() {
  return (
    <AppShell header={{ height: 64 }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Text fw={700} size="xl">Canvas</Text>
          <Badge variant="light">Development</Badge>
        </Group>
      </AppShell.Header>
      <AppShell.Main>
        <Container size="md" py="xl">
          <Paper withBorder p="xl" radius="md">
            <Stack>
              <Title order={1}>Collaborative artifact workspace</Title>
              <Text c="dimmed">
                React and Mantine are ready. Shared-state synchronization is the next
                step; editing and previews will come later.
              </Text>
            </Stack>
          </Paper>
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
