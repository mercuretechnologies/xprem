import { Component, ReactNode, useCallback, useEffect, useMemo, useState } from 'react';
import {
  ActivityIndicator,
  InteractionManager,
  Modal,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { BranchIcon } from './BranchIcon';
import { readConfig, readLoadedState, SurfConfig } from './config';
import { BranchPage, listBranches, surfTo } from './surf';
import { cardShadow, palette, radius, space, type } from './theme';
import { XpremMark } from './XpremMark';

/** "5 min ago" beats a timestamp for someone deciding what is fresh enough to test. */
function sinceLabel(iso: string): string {
  const published = Date.parse(iso);
  if (Number.isNaN(published)) {
    return '';
  }
  const minutes = Math.max(0, Math.round((Date.now() - published) / 60000));
  if (minutes < 1) return 'Updated just now';
  if (minutes < 60) return `Updated ${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `Updated ${hours} hr ago`;
  const days = Math.round(hours / 24);
  return days === 1 ? 'Updated yesterday' : `Updated ${days} days ago`;
}

/**
 * One probe per JS session. Memoised at module scope rather than in the component:
 * a useEffect would run again if the tree remounted, and this must be exactly once
 * per app open. reloadAsync starts a new session, so a surf re-probes naturally.
 *
 * Whether a channel allows surfing is a setting that changes whenever an admin
 * says so, while a manifest is a snapshot frozen when its update was served — so
 * the answer cannot ride in the manifest, or enabling the feature would only reach
 * devices that happen to download something afterwards.
 */
let sessionProbe: Promise<BranchPage | null> | null = null;

function probeOnce(config: SurfConfig): Promise<BranchPage | null> {
  // Only an ANSWER is remembered — a list, or the 404 that means surfing is off.
  // A timeout is not an answer: caching one would disable the picker until the
  // app is killed, and the tester has no way to know a retry would work.
  sessionProbe ??= listBranches(config).catch(error => {
    sessionProbe = null;
    throw error;
  });
  return sessionProbe;
}

let openPanel: (() => void) | null = null;

/** Opens the panel from anywhere — the edge handle is the built-in trigger. */
export function openControlCenter() {
  openPanel?.();
}

// A QA tool has no business taking the host app down: whatever throws inside the
// panel unmounts the panel, never the app around it.
class ControlCenterBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error) {
    console.warn(`[xprem] The control center crashed and was unmounted: ${error.message}`);
  }

  render() {
    return this.state.failed ? null : this.props.children;
  }
}

export function ControlCenter() {
  return (
    <ControlCenterBoundary>
      <ControlCenterPanel />
    </ControlCenterBoundary>
  );
}

function ControlCenterPanel() {
  const config = useMemo(readConfig, []);
  const loaded = useMemo(readLoadedState, []);
  const [visible, setVisible] = useState(false);
  const [page, setPage] = useState<BranchPage | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [expanding, setExpanding] = useState(false);
  const [expanded, setExpanded] = useState(false);

  const [allowed, setAllowed] = useState(false);
  const active = config !== null && allowed;
  const open = useCallback(() => setVisible(true), []);
  const close = useCallback(() => setVisible(false), []);

  // After first paint: a QA tool must not sit on the critical path of a launch.
  useEffect(() => {
    if (!config) return;
    let cancelled = false;
    const task = InteractionManager.runAfterInteractions(() => {
      probeOnce(config)
        .then(result => {
          if (cancelled || result === null) return;
          setAllowed(true);
          setPage(result);
        })
        .catch((cause: Error) => {
          // Not silent: the panel is unreachable for the rest of this session
          // unless the host app calls openControlCenter, and nothing else would
          // ever say why.
          console.warn(`[xprem] Could not reach the branch list: ${cause.message}`);
        });
    });
    return () => {
      cancelled = true;
      task.cancel();
    };
  }, [config]);

  useEffect(() => {
    // Registered whether or not the probe has answered. Gating it on `active`
    // made the host app's own trigger a silent no-op for the whole of every
    // launch, and permanently so whenever the probe failed — which is exactly
    // when someone reaches for it. Opening early shows the panel's own loading
    // and error states, which is the point.
    openPanel = open;
    return () => {
      // Only if it is still ours: a second instance may have taken over, and
      // clearing unconditionally would deregister a panel that is still mounted.
      if (openPanel === open) {
        openPanel = null;
      }
    };
  }, [open]);

  useEffect(() => {
    if (!visible || !config) return;
    const controller = new AbortController();
    setError(null);
    setNote(null);
    setQuery('');
    setExpanding(false);
    setExpanded(false);
    listBranches(config, controller.signal)
      .then(result => {
        if (result === null) {
          // Surfing was turned off while this session was running. Stand down
          // rather than spin: null reads as "loading" everywhere below.
          setAllowed(false);
          setVisible(false);
          return;
        }
        setPage(result);
      })
      .catch((cause: Error) => {
        if (cause.name !== 'AbortError') setError(cause.message);
      });
    return () => controller.abort();
  }, [visible, config]);

  const showEverything = useCallback(async () => {
    if (!config || expanded || expanding) return;
    setExpanding(true);
    try {
      const result = await listBranches(config, undefined, true);
      if (result) setPage(result);
      // Set even when the wide answer is itself capped: this only means "already
      // asked", so a keystroke cannot start the same fetch again. Whether the
      // list is COMPLETE is a separate question, read off total below.
      setExpanded(true);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setExpanding(false);
    }
  }, [config, expanded, expanding]);

  const search = useCallback(
    (text: string) => {
      setQuery(text);
      // Searching a partial list would answer "no match" for a branch that is
      // merely further down — a wrong answer, not a short one. So the first
      // keystroke pulls the rest; showEverything is a no-op once it has.
      if (text.length > 0) void showEverything();
    },
    [showEverything]
  );

  const switchTo = useCallback(
    async (branch: string | null) => {
      if (!config) return;
      setPending(branch ?? '');
      setNote(null);
      try {
        const outcome = await surfTo(config, branch);
        if (outcome === 'nothing-to-load') {
          setNote(
            branch
              ? `Switched to ${branch}. It has nothing newer than what is already ` +
                `running, so the screen did not change — you will get its next publish.`
              : 'Back on this build\u2019s own branch. Nothing newer to load.'
          );
        }
      } catch (cause) {
        setError((cause as Error).message);
      } finally {
        setPending(null);
      }
    },
    [config]
  );

  if (!active || !config) {
    return null;
  }

  // What is RUNNING, which after a surf is the surfed branch — not the branch the
  // channel maps to. Excluded from the list below: there is nothing to switch to.
  const loadedBranch = loaded.branch;
  const rows = (page?.branches ?? []).filter(candidate => candidate.name !== loadedBranch);
  // The server sends a short page of the newest branches. Say how many are left
  // rather than letting the list pass for the whole truth.
  const withheld = page ? page.total - page.branches.length : 0;
  // Keyed on the total, not the page: the page is short by design, so counting
  // loaded rows would hide the search field exactly when it is most needed.
  const searchable = (page?.total ?? 0) > 10;
  const needle = query.trim().toLowerCase();
  const shown = needle ? rows.filter(r => r.name.toLowerCase().includes(needle)) : rows;

  if (!visible) {
    // Always present while surfing is allowed: the way in a tester nobody briefed
    // will actually find. An edge sliver rather than an invisible corner target,
    // so it never swallows the host app's own controls.
    return (
      <Pressable
        style={styles.handle}
        onPress={open}
        hitSlop={{ top: 12, bottom: 12, left: 16, right: 8 }}
        accessibilityRole="button"
        accessibilityLabel="Open the branch picker">
        <View style={styles.handleGrip} />
      </Pressable>
    );
  }

  return (
    // pageSheet is the native card the dev menu uses: full-height sheet, the app
    // pushed back behind it, swipe down to dismiss. No custom animation to own.
    <Modal visible animationType="slide" presentationStyle="pageSheet" onRequestClose={close}>
      <View style={styles.screen}>
        <View style={styles.header}>
          <View style={styles.heroLine}>
            <BranchIcon size={26} weight={2.2} />
            <Text style={[type.hero, styles.heroName]} numberOfLines={1}>
              {loadedBranch ?? 'Embedded build'}
            </Text>
            <Pressable
              style={({ pressed }) => [styles.close, pressed && styles.closePressed]}
              onPress={close}
              accessibilityRole="button"
              accessibilityLabel="Close">
              <Text style={styles.closeGlyph}>✕</Text>
            </Pressable>
          </View>

          <View style={styles.chipLine}>
            <Text style={type.label}>Running on:</Text>
            <View style={styles.chip}>
              <View style={styles.liveDot} />
              <Text style={type.chip}>Channel: {config.channel}</Text>
            </View>
            <View style={[styles.chip, styles.chipNeutral]}>
              <Text style={[type.chip, styles.chipNeutralInk]}>
                Runtime {config.runtimeVersion}
              </Text>
            </View>
          </View>
        </View>

        <ScrollView style={styles.body} contentContainerStyle={styles.bodyContent}>
          {loaded.refusedBranch && (
            <View style={styles.warnCard}>
              <Text style={styles.warnTitle}>{loaded.refusedBranch} was rolled back</Text>
              <Text style={styles.warnBody}>
                It crashed on launch, so this build came back here. Publishing a fix to
                that branch makes it available again.
              </Text>
            </View>
          )}

          <Text style={[type.section, styles.sectionTitle]}>Switch to</Text>

          {searchable && (
            <TextInput
              style={styles.search}
              value={query}
              onChangeText={search}
              placeholder="Search branches"
              placeholderTextColor={palette.muted}
              autoCapitalize="none"
              autoCorrect={false}
              clearButtonMode="while-editing"
              accessibilityLabel="Search branches"
            />
          )}

          {page === null && (
            <View style={[styles.card, styles.cardCentered]}>
              <ActivityIndicator color={palette.muted} />
            </View>
          )}

          {page !== null && shown.length === 0 && (
            <View style={[styles.card, styles.cardCentered]}>
              <Text style={type.meta}>
                {needle
                  ? `No branch matches “${query.trim()}”.`
                  : `Nothing else is published for runtime ${config.runtimeVersion}.`}
              </Text>
            </View>
          )}

          {shown.map(candidate => (
            <Pressable
              key={candidate.name}
              style={({ pressed }) => [styles.card, styles.row, pressed && styles.rowPressed]}
              disabled={pending !== null}
              onPress={() => void switchTo(candidate.name)}>
              <BranchIcon size={22} color={palette.muted} />
              <View style={styles.rowText}>
                <Text style={type.rowTitle} numberOfLines={1}>
                  {candidate.name}
                </Text>
                <Text style={type.meta}>{sinceLabel(candidate.lastUpdateAt)}</Text>
              </View>
              {pending === candidate.name ? (
                <ActivityIndicator size="small" color={palette.muted} />
              ) : (
                <Text style={styles.chevron}>›</Text>
              )}
            </Pressable>
          ))}

          {withheld > 0 && !expanded && (
            <Pressable
              style={({ pressed }) => [styles.card, styles.seeAll, pressed && styles.rowPressed]}
              disabled={expanding}
              onPress={() => void showEverything()}>
              {expanding ? (
                <ActivityIndicator size="small" color={palette.muted} />
              ) : (
                <Text style={styles.seeAllText}>
                  Showing the {page?.branches.length} newest · See all {page?.total}
                </Text>
              )}
            </Pressable>
          )}

          {withheld > 0 && expanded && (
            // The server will not send more than this. Saying so is the whole
            // point: a search over a list this size must not read as exhaustive.
            <View style={[styles.card, styles.seeAll]}>
              <Text style={type.meta}>
                Showing the {page?.branches.length} newest of {page?.total}. Older
                branches are not listed.
              </Text>
            </View>
          )}

          {note && <Text style={[type.meta, styles.footnote]}>{note}</Text>}
          {error && <Text style={[type.meta, styles.errorText]}>{error}</Text>}

          <Pressable
            style={({ pressed }) => [styles.pill, pressed && styles.pillPressed]}
            disabled={pending !== null}
            onPress={() => void switchTo(null)}>
            {pending === '' ? (
              <ActivityIndicator size="small" color={palette.pillInk} />
            ) : (
              <Text style={type.pill}>Return to this build&rsquo;s branch</Text>
            )}
          </Pressable>

          <View style={styles.brand}>
            <XpremMark size={16} />
            <Text style={type.meta}>xprem</Text>
          </View>
        </ScrollView>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  handle: {
    position: 'absolute',
    right: 0,
    top: '45%',
    paddingVertical: space.md,
    paddingLeft: space.sm,
    paddingRight: 2,
  },
  handleGrip: {
    width: 4,
    height: 42,
    borderTopLeftRadius: 3,
    borderBottomLeftRadius: 3,
    backgroundColor: palette.ink,
    opacity: 0.35,
  },
  screen: { flex: 1, backgroundColor: palette.page },
  header: {
    backgroundColor: palette.surface,
    paddingHorizontal: space.lg,
    paddingTop: space.lg,
    paddingBottom: space.md,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: palette.border,
    gap: space.md,
  },
  heroLine: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  heroName: { flex: 1 },
  close: {
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: palette.page,
    alignItems: 'center',
    justifyContent: 'center',
  },
  closePressed: { backgroundColor: palette.surfacePressed },
  closeGlyph: { fontSize: 15, color: palette.muted },
  chipLine: { flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: space.sm },
  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    backgroundColor: palette.chipBg,
    borderRadius: radius.chip,
    paddingHorizontal: 10,
    paddingVertical: 5,
  },
  chipNeutral: { backgroundColor: palette.page },
  chipNeutralInk: { color: palette.muted },
  liveDot: { width: 7, height: 7, borderRadius: 4, backgroundColor: palette.live },
  body: { flex: 1 },
  bodyContent: { padding: space.md, paddingBottom: space.xl, gap: space.sm },
  sectionTitle: { marginTop: space.sm, marginLeft: space.xs },
  search: {
    backgroundColor: palette.surface,
    borderRadius: radius.chip + 2,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: palette.border,
    minHeight: 42,
    paddingHorizontal: space.md,
    fontSize: 16,
    color: palette.ink,
  },
  card: {
    backgroundColor: palette.surface,
    borderRadius: radius.card,
    paddingHorizontal: space.md,
    paddingVertical: 14,
    ...cardShadow,
  },
  cardCentered: { alignItems: 'center', justifyContent: 'center', minHeight: 64 },
  row: { flexDirection: 'row', alignItems: 'center', gap: space.sm + 4 },
  rowPressed: { backgroundColor: palette.surfacePressed },
  seeAll: { alignItems: 'center', justifyContent: 'center', minHeight: 48 },
  seeAllText: { fontSize: 15, fontWeight: '600', color: palette.ink },
  rowText: { flex: 1, gap: 2 },
  chevron: { fontSize: 22, color: palette.muted, marginTop: -2 },
  warnCard: {
    backgroundColor: palette.warnBg,
    borderRadius: radius.card,
    padding: space.md,
    gap: 4,
  },
  warnTitle: { fontSize: 16, fontWeight: '600', color: palette.warnInk },
  warnBody: { fontSize: 14, lineHeight: 20, color: palette.warnInk },
  footnote: { marginLeft: space.xs },
  errorText: { marginLeft: space.xs, color: palette.danger },
  pill: {
    marginTop: space.md,
    minHeight: 52,
    borderRadius: radius.pill,
    backgroundColor: palette.pill,
    alignItems: 'center',
    justifyContent: 'center',
  },
  pillPressed: { opacity: 0.85 },
  brand: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 6,
    marginTop: space.lg,
    opacity: 0.5,
  },
});

export default ControlCenter;
