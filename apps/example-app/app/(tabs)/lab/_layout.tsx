import { Stack } from 'expo-router';

export default function LabLayout() {
  return (
    <Stack>
      <Stack.Screen name="index" options={{ title: 'Lab' }} />
      <Stack.Screen name="slow" options={{ title: 'Slow screen' }} />
    </Stack>
  );
}
