import React, { useCallback, useEffect, useState } from 'react';
import { ActivityIndicator, RefreshControl, ScrollView, StyleSheet, Text, View } from 'react-native';

import { Button, Card, Label, Subtitle, Title } from '../components/ui';
import { api, Digest, Interaction } from '../api';
import { theme } from '../theme';

export function JournalScreen() {
  const [digest, setDigest] = useState<Digest | null>(null);
  const [history, setHistory] = useState<Interaction[]>([]);
  const [loading, setLoading] = useState(true);
  const [regenerating, setRegenerating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (refresh = false) => {
    setError(null);
    try {
      // L'historique est instantané, la synthèse interroge IMAP et Slack :
      // on ne fait pas attendre le premier sur le second.
      const [d, h] = await Promise.allSettled([api.digest(refresh), api.history()]);
      if (d.status === 'fulfilled') setDigest(d.value);
      else setError(d.reason?.message ?? 'synthèse indisponible');
      if (h.status === 'fulfilled') setHistory(h.value.interactions);
    } finally {
      setLoading(false);
      setRegenerating(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator color={theme.colors.accent} size="large" />
        <Text style={styles.loadingText}>Raoul fait le point…</Text>
      </View>
    );
  }

  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={styles.content}
      refreshControl={
        <RefreshControl
          refreshing={regenerating}
          onRefresh={() => {
            setRegenerating(true);
            void load(true);
          }}
          tintColor={theme.colors.accent}
        />
      }
    >
      <Title>Journal</Title>

      {error && (
        <Card style={{ borderColor: theme.colors.danger }}>
          <Text style={styles.error}>{error}</Text>
        </Card>
      )}

      <Card>
        <Label>Le point du jour</Label>
        <Text style={styles.summary}>
          {digest?.summary || "Pas encore de synthèse. Tire l'écran vers le bas pour en générer une."}
        </Text>
        {digest?.generated_at && (
          <Text style={styles.meta}>
            {digest.stale ? 'Dernière synthèse connue · ' : ''}
            {formatWhen(digest.generated_at)}
          </Text>
        )}
        {digest?.unavailable?.length ? (
          <Text style={styles.warn}>Sources indisponibles : {digest.unavailable.join(', ')}</Text>
        ) : null}
        <Button
          label="Régénérer la synthèse"
          variant="ghost"
          loading={regenerating}
          onPress={() => {
            setRegenerating(true);
            void load(true);
          }}
        />
      </Card>

      {digest && (
        <View style={styles.counters}>
          <Counter value={digest.events.length} label="aujourd'hui" />
          <Counter value={digest.emails.length} label="mails" />
          <Counter value={digest.slack.length} label="Slack" />
          <Counter value={digest.whatsapp.length} label="WhatsApp" />
        </View>
      )}

      {digest?.events.length ? (
        <Card>
          <Label>Agenda du jour</Label>
          {digest.events.map((e) => (
            <Text key={e.debut + e.titre} style={styles.row}>
              {formatHour(e.debut)}–{formatHour(e.fin)}  {e.titre}
              {e.lieu ? ` · ${e.lieu}` : ''}
            </Text>
          ))}
        </Card>
      ) : null}

      {digest?.emails.length ? (
        <Card>
          <Label>Mails non lus</Label>
          {digest.emails.map((m, i) => (
            <View key={`${m.recu}-${i}`} style={styles.item}>
              <Text style={styles.itemTitle}>{m.objet || '(sans objet)'}</Text>
              <Text style={styles.itemMeta}>
                {m.de} · {m.recu}
              </Text>
            </View>
          ))}
        </Card>
      ) : null}

      {digest?.slack.length ? (
        <Card>
          <Label>Slack</Label>
          {digest.slack.map((t, i) => (
            <View key={`${t.canal}-${i}`} style={styles.item}>
              <Text style={styles.itemTitle}>
                {t.canal}
                {t.non_lus ? ` · ${t.non_lus} non lus` : ''}
                {t.messages_recents ? ` · ${t.messages_recents} récents` : ''}
                {t.mentions ? ` · cité ${t.mentions}×` : ''}
              </Text>
              {t.extraits?.slice(0, 2).map((x) => (
                <Text key={x} style={styles.itemMeta}>
                  {x}
                </Text>
              ))}
            </View>
          ))}
        </Card>
      ) : null}

      {digest?.whatsapp.length ? (
        <Card>
          <Label>WhatsApp</Label>
          {digest.whatsapp.map((m, i) => (
            <View key={`${m.recu}-${i}`} style={styles.item}>
              <Text style={styles.itemTitle}>{m.de}</Text>
              <Text style={styles.itemMeta}>
                {m.message} · {m.recu}
              </Text>
            </View>
          ))}
        </Card>
      ) : null}

      <Label>Conversations</Label>
      {history.length === 0 ? (
        <Subtitle>Aucun échange pour l'instant.</Subtitle>
      ) : (
        history.map((it, i) => (
          <Card key={`${it.created_at}-${i}`}>
            <Text style={styles.meta}>{formatWhen(it.created_at)}</Text>
            <Text style={styles.question}>{it.transcript}</Text>
            <Text style={styles.answer}>{it.reply}</Text>
            {it.actions?.map((a, j) => (
              <Text key={j} style={styles.action}>
                ✓ {a.type === 'create_event' ? `« ${a.payload.title} » ajouté au calendrier` : a.type}
              </Text>
            ))}
          </Card>
        ))
      )}
    </ScrollView>
  );
}

function Counter({ value, label }: { value: number; label: string }) {
  return (
    <View style={styles.counter}>
      <Text style={styles.counterValue}>{value}</Text>
      <Text style={styles.counterLabel}>{label}</Text>
    </View>
  );
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  const time = d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
  return sameDay ? `aujourd'hui à ${time}` : `${d.toLocaleDateString('fr-FR')} à ${time}`;
}

function formatHour(iso: string): string {
  return new Date(iso).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: theme.colors.bg },
  content: { padding: theme.space(2.5), paddingBottom: theme.space(6), gap: theme.space(2) },
  center: {
    flex: 1,
    backgroundColor: theme.colors.bg,
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.space(2),
  },
  loadingText: { color: theme.colors.textMuted, fontSize: 15 },
  summary: { color: theme.colors.text, fontSize: 16, lineHeight: 24 },
  meta: { color: theme.colors.textMuted, fontSize: 11 },
  warn: { color: theme.colors.warning, fontSize: 12 },
  error: { color: theme.colors.danger, fontSize: 13 },
  counters: { flexDirection: 'row', gap: theme.space(1) },
  counter: {
    flex: 1,
    backgroundColor: theme.colors.surface,
    borderWidth: 1,
    borderColor: theme.colors.border,
    borderRadius: theme.radius.md,
    paddingVertical: theme.space(1.5),
    alignItems: 'center',
  },
  counterValue: { color: theme.colors.text, fontSize: 22, fontWeight: '700' },
  counterLabel: { color: theme.colors.textMuted, fontSize: 11 },
  row: { color: theme.colors.text, fontSize: 14, lineHeight: 22 },
  item: { gap: 2 },
  itemTitle: { color: theme.colors.text, fontSize: 14, fontWeight: '600' },
  itemMeta: { color: theme.colors.textMuted, fontSize: 12, lineHeight: 18 },
  question: { color: theme.colors.textMuted, fontSize: 14 },
  answer: { color: theme.colors.text, fontSize: 15, lineHeight: 22 },
  action: { color: theme.colors.success, fontSize: 12 },
});
