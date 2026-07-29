import { useEffect, useState } from 'react'
import { SafeAreaView, ScrollView, StyleSheet, TouchableOpacity } from 'react-native'
import { Observe, useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'

function Action({
  title,
  description,
  onPress,
}: {
  title: string
  description: string
  onPress: () => void
}) {
  return (
    <TouchableOpacity style={styles.action} onPress={onPress}>
      <ThemedText type="defaultSemiBold">{title}</ThemedText>
      <ThemedText style={styles.description}>{description}</ThemedText>
    </TouchableOpacity>
  )
}

export function LabScreen({
  onOpenSlow,
  onOpenModal,
}: {
  onOpenSlow: () => void
  onOpenModal: () => void
}) {
  const { markInteractive } = useObserve()
  const [crashOnRender, setCrashOnRender] = useState(false)

  useEffect(() => {
    markInteractive()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (crashOnRender) {
    // Caught by the app's ErrorBoundary, which logs it through Observe.
    throw new Error('Deliberate render crash from the observe lab')
  }

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView contentContainerStyle={styles.content}>
        <ThemedText type="title">Observe lab</ThemedText>

        <Action
          title="Open the slow screen"
          description="tti lands ~2s after ttr on /lab/slow"
          onPress={onOpenSlow}
        />
        <Action
          title="Open the modal"
          description="A route presented on top of the tabs"
          onPress={onOpenModal}
        />
        <Action
          title="Log an info event"
          description="Observe.logEvent with custom attributes"
          onPress={() =>
            Observe.logEvent('lab_button_pressed', {
              body: 'Info event from the lab screen',
              attributes: { source: 'lab', kind: 'info' },
            })
          }
        />
        <Action
          title="Log a warning event"
          description="Same event, severity warn"
          onPress={() =>
            Observe.logEvent('lab_button_pressed', {
              body: 'Warning event from the lab screen',
              attributes: { source: 'lab', kind: 'warn' },
              severity: 'warn',
            })
          }
        />
        <Action
          title="Throw during render"
          description="ErrorBoundary catches it and logs an event"
          onPress={() => setCrashOnRender(true)}
        />
        <Action
          title="Throw asynchronously"
          description="Uncaught error, captured by the global handler"
          onPress={() => {
            setTimeout(() => {
              throw new Error('Deliberate async crash from the observe lab')
            }, 0)
          }}
        />
        <Action
          title="Dispatch now"
          description="Flush pending metrics and logs to the server"
          onPress={() => Observe.dispatchEvents()}
        />
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
    gap: 8,
  },
  action: {
    padding: 16,
    borderRadius: 8,
    backgroundColor: '#f3f4f6',
  },
  description: {
    color: '#6b7280',
    fontSize: 14,
  },
})
