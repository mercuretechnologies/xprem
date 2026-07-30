import { useEffect } from 'react'
import { FlatList, SafeAreaView, StyleSheet, TouchableOpacity } from 'react-native'
import { useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'
import { CATALOG_ITEMS } from './catalog'

export function CatalogScreen({
  onOpenItem,
}: {
  onOpenItem: (id: string) => void
}) {
  const { markInteractive } = useObserve()

  useEffect(() => {
    markInteractive()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <SafeAreaView style={styles.container}>
      <FlatList
        data={CATALOG_ITEMS}
        keyExtractor={(item) => item.id}
        contentContainerStyle={styles.list}
        renderItem={({ item }) => (
          <TouchableOpacity
            style={styles.row}
            onPress={() => onOpenItem(item.id)}
          >
            <ThemedText type="defaultSemiBold">{item.title}</ThemedText>
            <ThemedText style={styles.subtitle}>{item.subtitle}</ThemedText>
          </TouchableOpacity>
        )}
      />
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  list: {
    padding: 16,
    gap: 8,
  },
  row: {
    padding: 16,
    borderRadius: 8,
    backgroundColor: '#f3f4f6',
  },
  subtitle: {
    color: '#6b7280',
    fontSize: 14,
  },
})
