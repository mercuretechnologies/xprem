import { useEffect } from 'react'
import { Button, SafeAreaView, StyleSheet } from 'react-native'
import { useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'

export function ModalScreen({ onClose }: { onClose: () => void }) {
  const { markInteractive } = useObserve()

  useEffect(() => {
    markInteractive()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <SafeAreaView style={styles.container}>
      <ThemedText type="title">Modal</ThemedText>
      <ThemedText>
        Presented above the tabs, so it reports its own route.
      </ThemedText>
      <Button title="Close" onPress={onClose} />
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 12,
    padding: 16,
  },
})
