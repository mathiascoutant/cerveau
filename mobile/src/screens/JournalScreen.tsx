import React, { useEffect, useState } from 'react';
import { ActivityIndicator, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Banner, Button, Card, Divider, EmptyState, ScreenHeader, SectionLabel, StatTile, Txt } from '../components/ui';
import { tabBarSpace } from '../components/TabBar';
import { api, Digest, Interaction, Provider } from '../api';
import { load, useSlot } from '../lib/cache';
import { alpha, theme } from '../theme';

/**
 * Le Journal ne se charge pas quand on ouvre son onglet — il se charge au
 * lancement de l'app, pendant qu'on regarde l'écran de Raoul. La synthèse
 * demande le modèle, donc plusieurs secondes : les attendre à chaque visite
 * était du temps perdu qu'aucune contrainte n'imposait.
 */
export function loadJournal(force = false) {
  return Promise.all([
    load<Digest>(DIGEST_KEY, () => api.digest(force), force),
    load<Interaction[]>(HISTORY_KEY, () => api.history().then((r) => r.interactions), force),
  ]);
}

export const DIGEST_KEY = 'digest';
const HISTORY_KEY = 'history';

export function JournalScreen() {
  const insets = useSafeAreaInsets();
  const digestSlot = useSlot<Digest>(DIGEST_KEY);
  const historySlot = useSlot<Interaction[]>(HISTORY_KEY);
  const [busy, setBusy] = useState(false);

  const digest = digestSlot.data;
  const history = historySlot.data ?? [];
  const error = digestSlot.error;

  useEffect(() => {
    void loadJournal();
  }, []);

  const refresh = () => {
    setBusy(true);
    void loadJournal(true).finally(() => setBusy(false));
  };

  // Le squelette n'apparaît que la toute première fois. Ensuite le contenu
  // précédent reste à l'écran pendant qu'on le remplace : un écran qui se vide
  // pour se recharger donne l'impression d'avoir perdu quelque chose.
  if (!digest && digestSlot.loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator color={theme.colors.primary} size="large" />
        <Txt variant="small" tone="faint">
          Raoul fait le point…
        </Txt>
      </View>
    );
  }

  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={[styles.content, { paddingBottom: tabBarSpace(insets.bottom) }]}
      refreshControl={
        <RefreshControl refreshing={busy} onRefresh={refresh} tintColor={theme.colors.primary} />
      }
    >
      <ScreenHeader title="Journal" subtitle="Ta journée, tes sources, tes échanges" />

      {error && (
        <Banner tone="danger" icon="alert-triangle">
          <Txt variant="small" tone="danger">
            {error}
          </Txt>
        </Banner>
      )}

      <Card>
        <View style={styles.cardHead}>
          <SectionLabel>Le point du jour</SectionLabel>
          {digest?.generated_at ? (
            <Txt variant="mono" tone="faint">
              {digest.stale ? 'daté · ' : ''}
              {formatWhen(digest.generated_at)}
            </Txt>
          ) : null}
        </View>

        <Txt variant="body">
          {digest?.summary || 'Aucune synthèse pour l’instant. Tire l’écran vers le bas pour en générer une.'}
        </Txt>

        {digest?.unavailable?.length ? (
          <View style={styles.inlineWarn}>
            <Feather name="alert-circle" size={13} color={theme.colors.warning} />
            <Txt variant="mono" tone="warning">
              Sources indisponibles : {digest.unavailable.join(', ')}
            </Txt>
          </View>
        ) : null}

        <Divider />
        <Button label="Régénérer" variant="ghost" icon="refresh-cw" loading={busy} onPress={refresh} />
      </Card>

      {/* Une source qu'il n'a pas branchée ne se compte pas : afficher
          « 0 WhatsApp » revient à lui parler de WhatsApp tous les matins. */}
      {digest && (
        <View style={styles.stats}>
          <StatTile value={digest.events.length} label="aujourd’hui" icon="calendar" />
          {has(digest, 'gandi') && <StatTile value={digest.emails.length} label="mails" icon="mail" />}
          {has(digest, 'slack') && <StatTile value={digest.slack.length} label="Slack" icon="hash" />}
          {has(digest, 'whatsapp') && (
            <StatTile value={digest.whatsapp.length} label="WhatsApp" icon="message-circle" />
          )}
        </View>
      )}

      {digest?.events.length ? (
        <Card>
          <SectionLabel>Agenda du jour</SectionLabel>
          {digest.events.map((e, i) => (
            <View key={`${e.debut}-${i}`} style={styles.eventRow}>
              <Txt variant="mono" tone="primary" style={styles.eventTime}>
                {formatHour(e.debut)}
              </Txt>
              <View style={styles.flex}>
                <Txt variant="bodyStrong">{e.titre}</Txt>
                <Txt variant="mono" tone="faint">
                  jusqu’à {formatHour(e.fin)}
                  {e.lieu ? ` · ${e.lieu}` : ''}
                </Txt>
              </View>
            </View>
          ))}
        </Card>
      ) : null}

      {digest?.emails.length ? (
        <Card>
          <SectionLabel>Mails non lus</SectionLabel>
          {digest.emails.map((m, i) => (
            <View key={`${m.recu}-${i}`} style={styles.item}>
              <Txt variant="bodyStrong" numberOfLines={2}>
                {m.objet || '(sans objet)'}
              </Txt>
              <Txt variant="mono" tone="faint">
                {m.de} · {m.recu}
              </Txt>
            </View>
          ))}
        </Card>
      ) : null}

      {digest?.slack.length ? (
        <Card>
          <SectionLabel>Slack</SectionLabel>
          {digest.slack.map((t, i) => (
            <View key={`${t.canal}-${i}`} style={styles.item}>
              <View style={styles.threadHead}>
                <Txt variant="bodyStrong" style={styles.flex} numberOfLines={1}>
                  {t.canal}
                </Txt>
                {t.mentions ? <Pill tone="warning" text={`cité ${t.mentions}×`} /> : null}
                {t.non_lus ? <Pill tone="primary" text={`${t.non_lus} non lus`} /> : null}
                {t.messages_recents ? <Pill tone="faint" text={`${t.messages_recents} récents`} /> : null}
              </View>
              {t.extraits?.slice(0, 2).map((x) => (
                <Txt key={x} variant="mono" tone="muted" numberOfLines={2}>
                  {x}
                </Txt>
              ))}
            </View>
          ))}
        </Card>
      ) : null}

      {digest?.whatsapp.length ? (
        <Card>
          <SectionLabel>WhatsApp</SectionLabel>
          {digest.whatsapp.map((t, i) => (
            <View key={`${t.conversation}-${i}`} style={styles.item}>
              <View style={styles.threadHead}>
                <Txt variant="bodyStrong" style={styles.flex} numberOfLines={1}>
                  {t.conversation}
                </Txt>
                {t.mentions ? <Pill tone="warning" text={`cité ${t.mentions}×`} /> : null}
                {t.non_lus ? <Pill tone="primary" text={`${t.non_lus} non lus`} /> : null}
              </View>
              {t.extraits?.slice(0, 2).map((x) => (
                <Txt key={x} variant="mono" tone="muted" numberOfLines={2}>
                  {x}
                </Txt>
              ))}
            </View>
          ))}
        </Card>
      ) : null}

      <SectionLabel>Conversations</SectionLabel>
      {history.length === 0 ? (
        <Card>
          <EmptyState
            icon="message-square"
            title="Aucun échange"
            message="Tes questions à Raoul et ses réponses apparaîtront ici."
          />
        </Card>
      ) : (
        history.map((it, i) => (
          <Card key={`${it.created_at}-${i}`}>
            <Txt variant="mono" tone="faint">
              {formatWhen(it.created_at)}
            </Txt>
            <Txt variant="small" tone="muted">
              {it.transcript}
            </Txt>
            <Txt variant="body">{it.reply}</Txt>
            {it.actions?.map((a, j) => (
              <View key={j} style={styles.inlineOk}>
                <Feather name="check-circle" size={13} color={theme.colors.success} />
                <Txt variant="mono" tone="success" style={styles.flex}>
                  {a.type === 'create_event'
                    ? `« ${a.payload.title} » ajouté au calendrier`
                    : a.type === 'email_draft'
                      ? `Réponse à ${a.payload.to} préparée`
                      : a.type === 'todo'
                        ? todoLabel(a.payload)
                        : a.type}
                </Txt>
              </View>
            ))}
          </Card>
        ))
      )}
    </ScrollView>
  );
}

/** Ce qu'une action de liste a changé, en une ligne. */
function todoLabel(payload: Record<string, any>): string {
  const title = `« ${payload.title} »`;
  switch (payload.state) {
    case 'done':
      return `${title} coché`;
    case 'moved':
      return `${title} déplacé${payload.when ? ` pour ${payload.when}` : ''}`;
    case 'dropped':
      return `${title} retiré de la liste`;
    default:
      return `${title} inscrit${payload.when ? ` pour ${payload.when}` : ' sans date'}`;
  }
}

/**
 * Dit si une source est branchée. Un digest d'avant cette version n'a pas le
 * champ : on affiche alors tout, comme avant, plutôt que de vider l'écran.
 */
function has(digest: Digest, provider: Provider): boolean {
  return !digest.sources || digest.sources.includes(provider);
}

function Pill({ text, tone }: { text: string; tone: 'primary' | 'warning' | 'faint' }) {
  const color =
    tone === 'warning'
      ? theme.colors.warning
      : tone === 'primary'
        ? theme.colors.primary
        : theme.colors.textFaint;
  return (
    <View style={[styles.pill, { borderColor: alpha(color, 0.35), backgroundColor: alpha(color, 0.12) }]}>
      <Txt variant="mono" style={{ color }}>
        {text}
      </Txt>
    </View>
  );
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  const time = d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
  return sameDay ? `aujourd’hui à ${time}` : `${d.toLocaleDateString('fr-FR')} à ${time}`;
}

function formatHour(iso: string): string {
  return new Date(iso).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  // Pas de fond : l'aurore peinte par App.tsx doit rester visible sous le
  // verre des cartes, sinon il n'y a plus rien à flouter.
  screen: { flex: 1 },
  content: {
    paddingHorizontal: theme.space.lg,
    paddingTop: theme.space.md,
    gap: theme.space.lg,
  },
  center: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.space.lg,
  },
  cardHead: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  stats: { flexDirection: 'row', gap: theme.space.sm },
  item: { gap: 3 },
  eventRow: { flexDirection: 'row', gap: theme.space.md, alignItems: 'flex-start' },
  eventTime: { width: 46, fontVariant: ['tabular-nums'] },
  threadHead: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
  pill: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: theme.radius.pill,
    paddingHorizontal: theme.space.sm,
    paddingVertical: 2,
  },
  inlineWarn: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
  inlineOk: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
});
