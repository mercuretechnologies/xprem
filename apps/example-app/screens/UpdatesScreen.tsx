import { useEffect, useState } from 'react'
import {
  ActivityIndicator,
  Platform,
  SafeAreaView,
  ScrollView,
  StyleSheet,
  TouchableOpacity,
  View,
} from 'react-native'
import * as Updates from 'expo-updates'
import Constants from 'expo-constants'
import * as Clipboard from 'expo-clipboard'
import { useObserve } from 'expo-observe'
import { ThemedText } from '@/components/ThemedText'

// The updates API is inert in development builds and on the web.
const updatesActive = !__DEV__ && Platform.OS !== 'web'

const shortId = (id?: string | null) => (id ? id.slice(0, 8) : '—')
const errorMessage = (error: unknown) =>
  error instanceof Error ? error.message : String(error)
const formatBytes = (bytes: number) =>
  bytes < 1024 * 1024
    ? `${Math.round(bytes / 1024)} KB`
    : `${(bytes / (1024 * 1024)).toFixed(1)} MB`
const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
const formatTime = (timestamp: number) =>
  new Date(timestamp).toLocaleTimeString([], { hour12: false })
// Newest first.
const readLogs = async () =>
  [...(await Updates.readLogEntriesAsync())].reverse()

type LogCategory = 'patch' | 'problem' | 'other' | 'state'
const LOG_CATEGORIES: LogCategory[] = ['patch', 'problem', 'other', 'state']
const CATEGORY_LABELS: Record<LogCategory, string> = {
  patch: 'Patch',
  problem: 'Problems',
  other: 'Other',
  state: 'State',
}
const categorize = (entry: Updates.UpdatesLogEntry): LogCategory => {
  if (['warn', 'error', 'fatal'].includes(entry.level)) return 'problem'
  if (entry.message.startsWith('Updates state change')) return 'state'
  if (/diff|patch/i.test(entry.message)) return 'patch'
  return 'other'
}
// One line per entry: the state machine dumps the whole manifest after its
// first line, and nobody reads that in a list.
const summarize = (entry: Updates.UpdatesLogEntry) => {
  const stateChange = /^Updates state change: (state = \w+, event = \w+)/.exec(
    entry.message,
  )
  if (stateChange) return stateChange[1]
  const firstLine = entry.message.split('\n')[0]
  return firstLine.length > 160 ? `${firstLine.slice(0, 160)}…` : firstLine
}

// What the server answers when asked for the new bundle the way the native
// client asks: with A-IM: bsdiff and the id of the update running now.
type ServerAnswer = {
  kind: 'patch' | 'full'
  bytes: number
  ms: number
  status: number
  baseUpdateId: string | null
}

async function askServerForBundle(
  currentUpdateId: string,
  available: Updates.UpdateInfoNew,
): Promise<ServerAnswer> {
  const launchAsset = (
    available.manifest as { launchAsset?: { url?: string } } | undefined
  )?.launchAsset
  if (!launchAsset?.url) {
    throw new Error('The manifest carries no launch asset URL')
  }
  const headers: Record<string, string> = {}
  for (const [key, value] of Object.entries(
    Constants.expoConfig?.updates?.requestHeaders ?? {},
  )) {
    if (typeof value === 'string') headers[key] = value
  }
  Object.assign(headers, {
    'A-IM': 'bsdiff',
    'Expo-Current-Update-ID': currentUpdateId,
    'Expo-Requested-Update-ID': available.updateId,
    'expo-platform': Platform.OS,
    'expo-runtime-version': Updates.runtimeVersion ?? '',
    'expo-protocol-version': '1',
  })
  const started = Date.now()
  const response = await fetch(launchAsset.url, { headers })
  // Read the body to measure it: the server streams without a content length.
  const body = await response.arrayBuffer()
  if (!response.ok && response.status !== 226) {
    throw new Error(`HTTP ${response.status}`)
  }
  const isPatch =
    response.headers.get('im') === 'bsdiff' || response.status === 226
  return {
    kind: isPatch ? 'patch' : 'full',
    bytes: body.byteLength,
    ms: Date.now() - started,
    status: response.status,
    baseUpdateId: response.headers.get('expo-base-update-id'),
  }
}

function Card({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <View style={styles.card}>
      <ThemedText type="defaultSemiBold">{title}</ThemedText>
      {children}
    </View>
  )
}

function Row({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <View style={styles.row}>
      <ThemedText style={styles.label}>{label}</ThemedText>
      <ThemedText style={[styles.value, mono && styles.mono]} selectable>
        {value}
      </ThemedText>
    </View>
  )
}

function Action({
  title,
  onPress,
  disabled,
  busy,
  secondary,
}: {
  title: string
  onPress: () => void
  disabled?: boolean
  busy?: boolean
  secondary?: boolean
}) {
  return (
    <TouchableOpacity
      style={[
        styles.button,
        secondary && styles.buttonSecondary,
        (disabled || busy) && styles.buttonDisabled,
      ]}
      onPress={onPress}
      disabled={disabled || busy}
    >
      {busy ? (
        <ActivityIndicator
          size="small"
          color={secondary ? '#111827' : '#fff'}
        />
      ) : (
        <ThemedText
          style={[styles.buttonText, secondary && styles.buttonTextSecondary]}
        >
          {title}
        </ThemedText>
      )}
    </TouchableOpacity>
  )
}

export function UpdatesScreen() {
  const { markInteractive } = useObserve()
  const {
    currentlyRunning,
    availableUpdate,
    downloadedUpdate,
    isChecking,
    isDownloading,
    checkError,
    downloadError,
  } = Updates.useUpdates()
  const [logs, setLogs] = useState<Updates.UpdatesLogEntry[]>([])
  const [copiedLog, setCopiedLog] = useState<number | null>(null)
  const [expandedLog, setExpandedLog] = useState<number | null>(null)
  const [hiddenCategories, setHiddenCategories] = useState<Set<LogCategory>>(
    () => new Set(['state']),
  )
  const [busy, setBusy] = useState(false)
  const [flowMessage, setFlowMessage] = useState<string | null>(null)
  const [download, setDownload] = useState<{
    ms: number
    applied: boolean
    problems: Updates.UpdatesLogEntry[]
  } | null>(null)
  const [answer, setAnswer] = useState<
    | { state: 'idle' | 'asking' }
    | { state: 'done'; result: ServerAnswer }
    | { state: 'error'; message: string }
  >({ state: 'idle' })

  const refreshLogs = async () => {
    try {
      setLogs(await readLogs())
    } catch (error) {
      console.error('Error fetching logs:', error)
    }
  }

  useEffect(() => {
    // The screen only shows anything useful once the update logs are in, so
    // that is what "interactive" means here.
    readLogs()
      .then(setLogs)
      .catch(error => console.error('Error fetching logs:', error))
      .finally(markInteractive)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // expo-updates writes "Applied diff" only after bspatch rebuilt the bundle
  // and its SHA-256 matched the manifest. The log survives a restart.
  const rebuiltFromPatch = logs.some(
    entry =>
      entry.message.startsWith('Applied diff') &&
      entry.updateId?.toLowerCase() ===
        currentlyRunning.updateId?.toLowerCase(),
  )
  const newUpdate =
    availableUpdate?.type === Updates.UpdateInfoType.NEW
      ? availableUpdate
      : undefined
  const bsdiffBuiltIn =
    Constants.expoConfig?.updates?.enableBsdiffPatchSupport ?? true

  const check = async () => {
    setBusy(true)
    setFlowMessage(null)
    setDownload(null)
    setAnswer({ state: 'idle' })
    try {
      const result = await Updates.checkForUpdateAsync()
      if (result.isRollBackToEmbedded) {
        setFlowMessage('The server asks to roll back to the embedded bundle.')
      } else if (!result.isAvailable) {
        setFlowMessage('Up to date.')
      }
    } catch (error) {
      setFlowMessage(errorMessage(error))
    } finally {
      setBusy(false)
      await refreshLogs()
    }
  }

  const fetchUpdate = async () => {
    setBusy(true)
    setFlowMessage(null)
    const started = Date.now()
    try {
      await Updates.fetchUpdateAsync()
      // Both platforms log "Applied diff for asset …" on success, and a
      // warning or error before retrying with the full bundle.
      const since = (await Updates.readLogEntriesAsync()).filter(
        entry => entry.timestamp >= started,
      )
      setDownload({
        ms: Date.now() - started,
        applied: since.some(entry => entry.message.startsWith('Applied diff')),
        problems: since.filter(entry =>
          ['warn', 'error', 'fatal'].includes(entry.level),
        ),
      })
    } catch (error) {
      setFlowMessage(errorMessage(error))
    } finally {
      setBusy(false)
      await refreshLogs()
    }
  }

  const ask = async () => {
    if (!newUpdate || !Updates.updateId) return
    setAnswer({ state: 'asking' })
    try {
      const result = await askServerForBundle(Updates.updateId, newUpdate)
      setAnswer({ state: 'done', result })
    } catch (error) {
      setAnswer({ state: 'error', message: errorMessage(error) })
    }
  }

  const toggleCategory = (category: LogCategory) =>
    setHiddenCategories(current => {
      const next = new Set(current)
      if (next.has(category)) next.delete(category)
      else next.add(category)
      return next
    })
  const visibleLogs = logs
    .map((entry, index) => ({ entry, index, category: categorize(entry) }))
    .filter(({ category }) => !hiddenCategories.has(category))

  const copyLog = async (entry: Updates.UpdatesLogEntry, index: number) => {
    await Clipboard.setStringAsync(JSON.stringify(entry, null, 2))
    setCopiedLog(index)
    setTimeout(() => setCopiedLog(null), 1500)
  }

  const flowStatus = isChecking
    ? 'Checking the server…'
    : isDownloading
    ? 'Downloading…'
    : downloadedUpdate
    ? 'Downloaded, restart to run it.'
    : newUpdate
    ? 'A newer update is available.'
    : availableUpdate
    ? 'A rollback to the embedded bundle is available.'
    : flowMessage ?? 'Not checked since the app started.'
  const flowError = checkError ?? downloadError

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView contentContainerStyle={styles.content}>
        <ThemedText type="title">Updates</ThemedText>
        <ThemedText style={styles.note}>
          {Constants.expoConfig?.updates?.url ?? 'No update server configured'}
        </ThemedText>
        {!updatesActive && (
          <ThemedText style={styles.note}>
            Updates are inactive in development builds and on the web.
          </ThemedText>
        )}

        <Card title="Running now">
          <Row label="Update" value={shortId(currentlyRunning.updateId)} mono />
          <Row
            label="Source"
            value={
              currentlyRunning.isEmbeddedLaunch
                ? 'Embedded bundle'
                : 'Downloaded update'
            }
          />
          {rebuiltFromPatch && (
            <Row
              label="Bundle"
              value="Rebuilt from a bsdiff patch, hash verified"
            />
          )}
          <Row
            label="Runtime version"
            value={currentlyRunning.runtimeVersion ?? '—'}
          />
          <Row label="Channel" value={currentlyRunning.channel ?? '—'} />
          {currentlyRunning.launchDuration != null && (
            <Row
              label="Launched in"
              value={formatDuration(
                Math.round(currentlyRunning.launchDuration),
              )}
            />
          )}
          {currentlyRunning.emergencyLaunchReason && (
            <Row
              label="Emergency launch"
              value={currentlyRunning.emergencyLaunchReason}
            />
          )}
        </Card>

        <Card title="Update flow">
          <ThemedText style={styles.status}>{flowStatus}</ThemedText>
          {flowError && (
            <ThemedText style={styles.error}>{flowError.message}</ThemedText>
          )}
          {flowMessage && flowStatus !== flowMessage && (
            <ThemedText style={styles.error}>{flowMessage}</ThemedText>
          )}
          {newUpdate && (
            <>
              <Row label="Available" value={shortId(newUpdate.updateId)} mono />
              <Row
                label="Published"
                value={newUpdate.createdAt.toLocaleString()}
              />
            </>
          )}
          {download && (
            <>
              <Row label="Downloaded in" value={formatDuration(download.ms)} />
              <Row
                label="Patch"
                value={
                  download.applied
                    ? 'Applied'
                    : download.problems.length > 0
                    ? 'Fell back to the full bundle'
                    : 'Not used'
                }
              />
              {download.problems.map(entry => (
                <ThemedText key={entry.timestamp} style={styles.error}>
                  {entry.message}
                </ThemedText>
              ))}
            </>
          )}
          <View style={styles.actions}>
            <Action
              title="Check"
              onPress={check}
              busy={busy && isChecking}
              disabled={!updatesActive || busy}
            />
            {availableUpdate && !downloadedUpdate && (
              <Action
                title="Download"
                onPress={fetchUpdate}
                busy={busy && isDownloading}
                disabled={!updatesActive || busy}
              />
            )}
            {downloadedUpdate && (
              <Action
                title="Restart"
                onPress={() => Updates.reloadAsync()}
                disabled={!updatesActive || busy}
              />
            )}
          </View>
        </Card>

        <Card title="Bundle diffing">
          <Row
            label="This build"
            value={bsdiffBuiltIn ? 'Accepts patches' : 'Full bundles only'}
          />
          <ThemedText>
            Built from updates.enableBsdiffPatchSupport in app.config.ts. Set
            DISABLE_BSDIFF=true at build time to turn it off.
          </ThemedText>
          {newUpdate ? (
            <>
              <ThemedText style={styles.note}>
                Asks the server for the new bundle exactly as the native client
                does, with A-IM: bsdiff and the running update as base. The
                answer is downloaded once to measure it.
              </ThemedText>
              {answer.state === 'done' && (
                <>
                  <Row
                    label="Asked with base"
                    value={shortId(Updates.updateId)}
                    mono
                  />
                  <Row
                    label="Server sends"
                    value={
                      answer.result.kind === 'patch'
                        ? `bsdiff patch, ${formatBytes(answer.result.bytes)}`
                        : `full bundle, ${formatBytes(
                            answer.result.bytes,
                          )} uncompressed`
                    }
                  />
                  <Row
                    label="Patch base"
                    value={shortId(answer.result.baseUpdateId)}
                    mono
                  />
                  <Row
                    label="Response"
                    value={`HTTP ${answer.result.status} in ${formatDuration(
                      answer.result.ms,
                    )}`}
                  />
                </>
              )}
              {answer.state === 'error' && (
                <ThemedText style={styles.error}>{answer.message}</ThemedText>
              )}
              <View style={styles.actions}>
                <Action
                  title="Ask the server"
                  onPress={ask}
                  busy={answer.state === 'asking'}
                  disabled={!Updates.updateId}
                  secondary
                />
              </View>
            </>
          ) : (
            <ThemedText style={styles.note}>
              Check for an update first: the server is asked for the bundle of
              the update it offers.
            </ThemedText>
          )}
        </Card>

        <Card title="expo-updates log">
          <View style={styles.actions}>
            {LOG_CATEGORIES.map(category => {
              const active = !hiddenCategories.has(category)
              const count = logs.filter(
                entry => categorize(entry) === category,
              ).length
              return (
                <TouchableOpacity
                  key={category}
                  style={[styles.chip, active && styles.chipActive]}
                  onPress={() => toggleCategory(category)}
                >
                  <ThemedText
                    style={[styles.chipText, active && styles.chipTextActive]}
                  >
                    {CATEGORY_LABELS[category]} {count}
                  </ThemedText>
                </TouchableOpacity>
              )
            })}
          </View>
          {visibleLogs.length === 0 ? (
            <ThemedText style={styles.note}>No entry.</ThemedText>
          ) : (
            visibleLogs.map(({ entry, index, category }) => {
              const expanded = expandedLog === index
              return (
                <TouchableOpacity
                  key={`${entry.timestamp}-${index}`}
                  style={styles.logRow}
                  onPress={() => setExpandedLog(expanded ? null : index)}
                >
                  <ThemedText style={styles.logMeta}>
                    {formatTime(entry.timestamp)} · {CATEGORY_LABELS[category]}{' '}
                    · {entry.level.toUpperCase()} · {entry.code}
                  </ThemedText>
                  <ThemedText
                    style={[
                      styles.logMessage,
                      category === 'problem' && styles.error,
                      entry.message.startsWith('Applied diff') &&
                        styles.success,
                    ]}
                  >
                    {expanded ? entry.message : summarize(entry)}
                  </ThemedText>
                  {expanded && (
                    <TouchableOpacity onPress={() => copyLog(entry, index)}>
                      <ThemedText style={styles.link}>
                        {copiedLog === index ? 'Copied' : 'Copy JSON'}
                      </ThemedText>
                    </TouchableOpacity>
                  )}
                </TouchableOpacity>
              )
            })
          )}
          <View style={styles.actions}>
            <Action title="Refresh" onPress={refreshLogs} secondary />
            <Action
              title="Clear"
              onPress={async () => {
                await Updates.clearLogEntriesAsync()
                await refreshLogs()
              }}
              disabled={!updatesActive}
              secondary
            />
          </View>
        </Card>
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
  card: {
    padding: 16,
    borderRadius: 12,
    backgroundColor: '#f3f4f6',
    gap: 8,
  },
  row: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
    gap: 12,
  },
  label: {
    color: '#6b7280',
    fontSize: 14,
    lineHeight: 20,
  },
  value: {
    flexShrink: 1,
    fontSize: 14,
    lineHeight: 20,
    textAlign: 'right',
  },
  mono: {
    fontFamily: 'SpaceMono',
    fontSize: 13,
  },
  status: {
    fontSize: 15,
    lineHeight: 22,
  },
  note: {
    color: '#6b7280',
    fontSize: 13,
    lineHeight: 18,
  },
  error: {
    color: '#b91c1c',
    fontSize: 13,
    lineHeight: 18,
  },
  actions: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    marginTop: 4,
  },
  button: {
    minWidth: 96,
    alignItems: 'center',
    paddingVertical: 10,
    paddingHorizontal: 14,
    borderRadius: 8,
    backgroundColor: '#2563eb',
  },
  buttonSecondary: {
    backgroundColor: '#e5e7eb',
  },
  buttonDisabled: {
    opacity: 0.5,
  },
  buttonText: {
    color: '#fff',
    fontSize: 14,
    lineHeight: 20,
    fontWeight: '600',
  },
  buttonTextSecondary: {
    color: '#111827',
  },
  logRow: {
    paddingVertical: 8,
    borderTopWidth: 1,
    borderTopColor: '#e5e7eb',
    gap: 2,
  },
  logMeta: {
    color: '#6b7280',
    fontSize: 12,
    lineHeight: 16,
  },
  logMessage: {
    fontSize: 13,
    lineHeight: 18,
  },
  success: {
    color: '#047857',
  },
  link: {
    marginTop: 4,
    color: '#2563eb',
    fontSize: 12,
    lineHeight: 16,
  },
  chip: {
    paddingVertical: 6,
    paddingHorizontal: 10,
    borderRadius: 999,
    backgroundColor: '#e5e7eb',
  },
  chipActive: {
    backgroundColor: '#111827',
  },
  chipText: {
    color: '#374151',
    fontSize: 12,
    lineHeight: 16,
  },
  chipTextActive: {
    color: '#fff',
  },
})
