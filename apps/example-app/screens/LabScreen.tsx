import { useEffect, useState } from 'react'
import { ScrollView, StyleSheet, TouchableOpacity } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import { Observe, useObserve } from 'expo-observe'

import { ThemedText } from '@/components/ThemedText'

// Not a tail call, so no engine can fold the frames away.
function descend(depth: number): number {
  if (depth === 0) {
    throw new Error('Deliberate deep crash from the observe lab')
  }
  return descend(depth - 1) + 1
}

function recurseForever(): number {
  return recurseForever() + 1
}

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
          title="Report a caught error"
          description="js.exception, not fatal, source reportedByUser"
          onPress={() => {
            try {
              throw new Error('Deliberate caught error from the observe lab')
            } catch (error) {
              Observe.reportError(error)
            }
          }}
        />
        <Action
          title="Throw 300 frames deep"
          description="Reported error whose stack is 300 descend() frames: 50 recent + 50 oldest are kept"
          onPress={() => {
            try {
              descend(300)
            } catch (error) {
              Observe.reportError(error)
            }
          }}
        />
        <Action
          title="Overflow the call stack"
          description="Infinite recursion, RangeError: does Hermes skip frames itself?"
          onPress={() => {
            try {
              recurseForever()
            } catch (error) {
              Observe.reportError(error)
            }
          }}
        />
        <Action
          title="Throw a non-fatal error"
          description="Global handler called with isFatal=false"
          onPress={() =>
            ErrorUtils.getGlobalHandler()(
              new Error('Deliberate non-fatal error from the observe lab'),
              false
            )
          }
        />
        <Action
          title="Reject a promise without catch"
          description="Unhandled rejection: the SDK captures nothing today"
          onPress={() => {
            Promise.reject(new Error('Deliberate unhandled rejection from the observe lab'))
          }}
        />
        <Action
          title="Read a property of undefined"
          description="TypeError in a press handler, fatal, the most common crash in the wild"
          onPress={() => {
            const user = undefined as unknown as { profile: { name: string } }
            console.log(user.profile.name)
          }}
        />
        <Action
          title="Throw an error with a cause"
          description="Fatal, nested error: does the cause survive the trip?"
          onPress={() => {
            setTimeout(() => {
              throw new Error('Checkout failed', {
                cause: new Error('Payment provider timed out'),
              })
            }, 0)
          }}
        />
        <Action
          title="Fetch a 500"
          description="Failed request, shows up in network traces, not as an error"
          onPress={() => {
            fetch('https://httpstat.us/500').catch(() => {})
          }}
        />
        <Action
          title="console.error"
          description="Logged to the console only: not captured, unlike Sentry breadcrumbs"
          onPress={() => console.error('Deliberate console.error from the observe lab')}
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
