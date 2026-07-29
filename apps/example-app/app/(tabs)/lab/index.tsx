import { useRouter } from 'expo-router';

import { LabScreen } from '@/screens/LabScreen';

export default function LabRoute() {
  const router = useRouter();
  return (
    <LabScreen
      onOpenSlow={() => router.push('/lab/slow')}
      onOpenModal={() => router.push('/modal')}
    />
  );
}
