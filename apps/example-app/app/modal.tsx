import { useRouter } from 'expo-router';

import { ModalScreen } from '@/screens/ModalScreen';

export default function ModalRoute() {
  const router = useRouter();
  return <ModalScreen onClose={() => router.back()} />;
}
