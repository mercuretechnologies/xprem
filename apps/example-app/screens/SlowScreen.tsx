import { useEffect, useState } from 'react'
import { ActivityIndicator, SafeAreaView, StyleSheet } from 'react-native'
import { useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'

const LOADING_MS = 2000

/**
 * Renders immediately but only becomes usable two seconds later, so `tti` lands
 * well above `cold_ttr`/`warm_ttr` for this route. Useful to check the server
 * really keeps the two metrics apart per route.
 */
export function SlowScreen() {
  const { markInteractive } = useObserve()
  const [ready, setReady] = useState(false)

  useEffect(() => {
    const timer = setTimeout(() => {
      setReady(true)
      markInteractive({ params: { simulatedLoadMs: LOADING_MS } })
    }, LOADING_MS)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <SafeAreaView style={styles.container}>
      {ready ? (
        <>
          <ThemedText type="subtitle">Interactive</ThemedText>
          <ThemedText>
            markInteractive() fired {LOADING_MS}ms after the screen was focused.
          </ThemedText>
        </>
      ) : (
        <>
          <ActivityIndicator size="large" />
          <ThemedText>Pretending to load…</ThemedText>
        </>
      )}
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
