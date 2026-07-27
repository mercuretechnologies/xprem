import {
  Platform,
  SafeAreaView,
  ScrollView,
  Alert,
  StyleSheet,
  Button,
  ActivityIndicator,
  View,
} from 'react-native'
import * as Updates from 'expo-updates'
import { ThemedText } from '@/components/ThemedText'
import { ThemedView } from '@/components/ThemedView'
import Constants from 'expo-constants'
import { useState, useEffect } from 'react'
import { UpdatesLogViewer } from '@/components/LogViewer'



export default function HomeScreen() {
  const [loading, load] = useState<boolean>(false)
  const [logs, setLogs] = useState<Updates.UpdatesLogEntry[]>([])


  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const randomCrash = () => {
    // Random throw an error 50% of time
    if (Math.random() < 0.5) {
      throw new Error('Random crash!')
    }
  }

  useEffect(() => {
    const fetchLogs = async () => {
      try {
        const logEntries = await Updates.readLogEntriesAsync()
        setLogs(logEntries)
      } catch (error) {
        console.error('Error fetching logs:', error)
      }
    }

    fetchLogs()
    // randomCrash()
  }, [])


  const checkUpdates = async () => {
    if (__DEV__ || loading || Platform.OS === 'web') {
      return
    }
    try {
      await Updates.clearLogEntriesAsync()
      const update = await Updates.checkForUpdateAsync()
      const logEntries = await Updates.readLogEntriesAsync()
      if (update.isAvailable) {
        load(true)
        await Updates.fetchUpdateAsync()
        setLogs(logEntries)
        load(false)
        await Updates.reloadAsync()
      } else if (update.isRollBackToEmbedded) {
        setLogs(logEntries)
        load(true)

        await Updates.reloadAsync()
        // add alert on rollback
        load(false)
        Alert.alert(
          'Update rolled back',
          'The update was rolled back to the embedded version.',
          [
            {
              text: 'OK',
              style: 'cancel',
            },
          ],
          { cancelable: false },
        )
      } else {
        setLogs(logEntries)
        load(false)
        Alert.alert(
          'Update not available',
          'There is no new update available.',
          [
            {
              text: 'OK',
              style: 'cancel',
            },
          ],
          { cancelable: false },
        )
      }
    } catch {
      load(false)
    }
  }

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1 }}>
        <View
          style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}
        >
          <ActivityIndicator size="large" color="#0000ff" />
        </View>
      </SafeAreaView>
    )
  }

  return (
    <SafeAreaView style={styles.safeAreaView}>
      <ScrollView contentContainerStyle={styles.scrollView}>
        <ThemedView style={styles.titleContainer}>
          <ThemedText type="title">Current update:</ThemedText>
        </ThemedView>
        <ThemedView style={styles.informations}>
          <ThemedText>Update ID: {Updates.updateId}</ThemedText>
          <ThemedText>Runtime version: {Updates.runtimeVersion}</ThemedText>
          <ThemedText>Release channel: {Updates.channel}</ThemedText>
          <ThemedText>
            Update server url : {Constants.expoConfig?.updates?.url || ''}
          </ThemedText>
        </ThemedView>
        <ThemedView>
          <Button
            title="Check for updates"
            onPress={() => checkUpdates()}
            disabled={loading}
          />
          <UpdatesLogViewer logs={logs} />
        </ThemedView>
      </ScrollView>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  titleContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  informations: {
    gap: 8,
    marginBottom: 8,
  },
  safeAreaView: {
    flex: 1,
    backgroundColor: '#fff',
  },
  scrollView: {
    flexGrow: 1,
    paddingVertical: 16,
    paddingHorizontal: 16,
  },
})
