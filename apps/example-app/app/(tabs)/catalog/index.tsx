import { useRouter } from 'expo-router';

import { CatalogScreen } from '@/screens/CatalogScreen';

export default function CatalogRoute() {
  const router = useRouter();
  return (
    <CatalogScreen
      onOpenItem={(id) => router.push(`/catalog/${id}?from=list`)}
    />
  );
}
