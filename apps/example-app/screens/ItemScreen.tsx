import { useEffect } from 'react'
import { SafeAreaView, ScrollView, StyleSheet } from 'react-native'
import { useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'
import { findCatalogItem } from './catalog'

export function ItemScreen({
  id,
  params,
}: {
  id: string
  /** Raw params as the navigation framework saw them, echoed so they can be
   * compared with the `routeParams` attribute that lands on the server. */
  params: Record<string, unknown>
}) {
  const { markInteractive } = useObserve()
  const item = findCatalogItem(id)

  useEffect(() => {
    markInteractive()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView contentContainerStyle={styles.content}>
        <ThemedText type="title">{item?.title ?? `Item ${id}`}</ThemedText>
        <ThemedText>{item?.subtitle ?? 'Unknown item'}</ThemedText>
        <ThemedText type="subtitle">Route params</ThemedText>
        <ThemedText style={styles.code}>
          {JSON.stringify(params, null, 2)}
        </ThemedText>
      </ScrollView>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  content: {
    padding: 16,
    gap: 12,
  },
  code: {
    fontFamily: 'SpaceMono',
    fontSize: 13,
    backgroundColor: '#f3f4f6',
    padding: 12,
    borderRadius: 8,
  },
})
