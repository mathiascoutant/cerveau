import React, { useCallback, useEffect, useState } from 'react';
import { Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Badge, Corners, IconName, Notice, ScreenHeader, Skeleton, T } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { api, JobCard } from '../api';

const ICONS: Record<string, IconName> = { csp: 'tag', flight_schedule: 'navigation' };

/**
 * L'onglet Jobs : les outils métier, une carte chacun.
 *
 * Le serveur dit si une carte s'ouvre. CSP ne s'ouvre que si Tuleap est
 * configuré ET que la clé est saisie ; Flight Schedule est là pour dire qu'il
 * viendra, et il ne s'ouvre pas — pas de faux écran derrière une vraie carte.
 */
export function JobsScreen({ onOpen }: { onOpen: (key: string) => void }) {
  const insets = useSafeAreaInsets();
  const [jobs, setJobs] = useState<JobCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const res = await api.jobs();
      setJobs(res.jobs);
    } catch (err) {
      setError((err as Error).message);
      setJobs((prev) => prev ?? fallbackCards());
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      refreshControl={
        <RefreshControl
          refreshing={busy}
          onRefresh={() => {
            setBusy(true);
            void load();
          }}
          tintColor={tokens.colors.accent}
        />
      }
    >
      <ScreenHeader title="Jobs" subtitle="tes outils métier" />

      {error ? (
        <Notice tone="warn" icon="wifi-off" title="Serveur injoignable">
          <T v="small" tone="muted">
            {error}
          </T>
        </Notice>
      ) : null}

      {jobs === null ? (
        <>
          <Skeleton lines={2} style={styles.skeleton} />
          <Skeleton lines={2} style={styles.skeleton} />
        </>
      ) : (
        jobs.map((job) => <Card key={job.key} job={job} onPress={() => onOpen(job.key)} />)
      )}
    </ScrollView>
  );
}

function fallbackCards(): JobCard[] {
  return [
    { key: 'csp', title: 'CSP', description: 'Accès à mes tickets CSP Tuleap', enabled: false, configured: false, connected: false, reason: 'Serveur injoignable.' },
    { key: 'flight_schedule', title: 'Flight Schedule', description: 'Accès au planning des vols', enabled: false, configured: false, connected: false, reason: 'Bientôt disponible.' },
  ];
}

function Card({ job, onPress }: { job: JobCard; onPress: () => void }) {
  const on = job.enabled;
  const color = on ? tokens.colors.accent : tokens.colors.text3;
  const soon = job.key === 'flight_schedule';
  return (
    <Pressable
      onPress={() => {
        if (!on) return;
        void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
        onPress();
      }}
      disabled={!on}
      accessibilityRole="button"
      accessibilityLabel={`${job.title} : ${job.description}`}
      accessibilityState={{ disabled: !on }}
      accessibilityHint={on ? 'Ouvre la vue dédiée' : job.reason}
      style={({ pressed }) => [styles.card, !on && styles.cardOff, pressed && on && styles.pressed]}
    >
      <Corners color={on ? alpha(tokens.colors.accent, 0.7) : tokens.colors.line} />
      <View style={[styles.icon, { borderColor: alpha(color, 0.5), backgroundColor: alpha(color, 0.08) }]}>
        <Feather name={ICONS[job.key] ?? 'box'} size={22} color={color} />
      </View>
      <View style={styles.body}>
        <View style={styles.titleRow}>
          <T v="h" tone={on ? 'default' : 'muted'}>
            {job.title}
          </T>
          {soon ? <Badge label="bientôt" tone="faint" /> : on ? <Badge label="actif" tone="ok" /> : <Badge label="à configurer" tone="warn" />}
        </View>
        <T v="small" tone={on ? 'muted' : 'faint'}>
          {job.description}
        </T>
        {!on && job.reason ? (
          <T v="mono" tone="faint">
            {job.reason}
          </T>
        ) : null}
      </View>
      <Feather name="chevron-right" size={18} color={on ? tokens.colors.text2 : tokens.colors.line} />
    </Pressable>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.md },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  pressed: { opacity: 0.7 },
  card: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: tokens.space.lg,
    padding: tokens.space.lg,
    borderWidth: 1,
    borderColor: tokens.colors.lineStrong,
    borderRadius: tokens.radius.lg,
    backgroundColor: tokens.colors.panel,
  },
  cardOff: { borderColor: tokens.colors.line, backgroundColor: tokens.colors.bg },
  icon: { width: 52, height: 52, borderRadius: tokens.radius.md, borderWidth: 1, alignItems: 'center', justifyContent: 'center' },
  body: { flex: 1, gap: 4 },
  titleRow: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm, flexWrap: 'wrap' },
});
