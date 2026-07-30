import { useLocalSearchParams } from 'expo-router';

import { ItemScreen } from '@/screens/ItemScreen';

export default function ItemRoute() {
  const params = useLocalSearchParams<{ id: string }>();
  return <ItemScreen id={params.id} params={params} />;
}
