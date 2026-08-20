import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useKeepAwake } from 'expo-keep-awake';

import { Card, Field, StatusDot, Subtitle } from '../components/ui';
import { useRaoul, RaoulState } from '../hooks/useRaoul';
import { api, SourceStatus } from '../api';
import { theme } from '../theme';

const STATE_LABEL: Record<RaoulState, string> = {
  off: 'Micro coupé',
  waiting: 'Dis « OK Raoul »',
  listening: 'Je t\'écoute…',
  thinking: 'Je regarde ton agenda et tes messages…',
  speaking: 'Raoul répond',
};

const SOURCE_LABEL: Record<string, string> = {
  gandi: 'Mails',
  slack: 'Slack',
  whatsapp: 'WhatsApp',
  calendar: 'Agenda',
};

export function AssistantScreen() {
  const { state, partial, error, history, start, stop, pushToTalk, askText, voiceAvailable } =
    useRaoul();
  const [sources, setSources] = useState<SourceStatus[]>([]);
  const [draft, setDraft] = useState('');

  // Tant que Raoul écoute, l'écran ne doit pas s'éteindre (l'écoute en fond
  // reste active grâce au mode audio, mais l'écran allumé évite les surprises).
  useKeepAwake();

  useEffect(() => {
    let alive = true;
    api
      .status()
      .then((res) => alive && setSources(res.sources))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, [state === 'waiting']);

  const busy = state === 'thinking' || state === 'speaking';
  const active = state !== 'off';

  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={styles.content}
      keyboardShouldPersistTaps="handled"
    >
      <View style={styles.header}>
        <Text style={styles.brand}>Raoul</Text>
        <Text style={styles.tagline}>Ton agenda, tes mails, Slack et WhatsApp — d'une seule voix.</Text>
      </View>

      <View style={styles.sources}>
        {sources.map((s) => (
          <View key={s.provider} style={styles.sourceChip}>
            <StatusDot ok={s.connected} warn={Boolean(s.error)} />
            <Text style={styles.sourceLabel}>{SOURCE_LABEL[s.provider] ?? s.provider}</Text>
            {s.connected && s.unread > 0 && <Text style={styles.sourceCount}>{s.unread}</Text>}
          </View>
        ))}
      </View>

      <Pressable
        onPress={() => (active ? stop() : start())}
        onLongPress={pushToTalk}
        disabled={!voiceAvailable && !active}
        style={({ pressed }) => [
          styles.orb,
          active && styles.orbActive,
          state === 'listening' && styles.orbListening,
          pressed && { transform: [{ scale: 0.97 }] },
        ]}
      >
        {busy ? (
          <ActivityIndicator color={theme.colors.text} size="large" />
        ) : (
          <Text style={styles.orbIcon}>{active ? '◉' : '◎'}</Text>
        )}
      </Pressable>

      <Text style={styles.state}>{voiceAvailable ? STATE_LABEL[state] : 'Mode texte'}</Text>
      {state === 'listening' && partial.length > 0 && (
        <Text style={styles.partial}>« {partial} »</Text>
      )}
      {!voiceAvailable ? (
        <Card>
          <Text style={styles.noticeTitle}>Expo Go — aperçu de l'interface</Text>
          <Subtitle>
            « OK Raoul » et l'agenda reposent sur des modules natifs qu'Expo Go n'embarque pas.
            Tout le reste fonctionne : écris ta demande ci-dessous, Raoul répond et lit sa réponse
            à voix haute. Pour le vocal complet, lance un dev build.
          </Subtitle>
        </Card>
      ) : (
        !active && (
          <Subtitle>
            Touche le cercle pour activer l'écoute permanente. Appui long : parler tout de suite,
            sans dire le mot d'activation.
          </Subtitle>
        )
      )}

      {error && (
        <Card style={{ borderColor: theme.colors.danger }}>
          <Text style={styles.error}>{error}</Text>
        </Card>
      )}

      <View style={styles.typeRow}>
        <Field
          placeholder="…ou écris ta demande"
          value={draft}
          onChangeText={setDraft}
          onSubmitEditing={() => {
            const text = draft.trim();
            if (!text) return;
            setDraft('');
            void askText(text);
          }}
          returnKeyType="send"
          style={{ flex: 1 }}
        />
      </View>

      {history.map((item) => (
        <Card key={item.id}>
          <Text style={styles.question}>{item.question}</Text>
          <Text style={styles.answer}>{item.answer}</Text>
          {item.effects?.map((effect) => (
            <Text key={effect} style={styles.effect}>
              ✓ {effect}
            </Text>
          ))}
          {item.steps && item.steps.length > 0 && (
            <Text style={styles.steps}>Consulté : {[...new Set(item.steps)].join(', ')}</Text>
          )}
        </Card>
      ))}

      {history.length === 0 && (
        <Card>
          <Text style={styles.exampleTitle}>Par exemple</Text>
          <Text style={styles.example}>« OK Raoul, je peux aller faire du sport à 10h demain ? »</Text>
          <Text style={styles.example}>« OK Raoul, est-ce que j'ai raté quelque chose d'urgent ? »</Text>
          <Text style={styles.example}>« OK Raoul, bloque-moi deux heures jeudi après-midi. »</Text>
        </Card>
      )}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: theme.colors.bg },
  content: { padding: theme.space(2.5), paddingBottom: theme.space(6), gap: theme.space(2) },
  header: { gap: theme.space(0.5), marginTop: theme.space(1) },
  brand: { color: theme.colors.text, fontSize: 34, fontWeight: '800', letterSpacing: -1 },
  tagline: { color: theme.colors.textMuted, fontSize: 14, lineHeight: 20 },
  sources: { flexDirection: 'row', flexWrap: 'wrap', gap: theme.space(1) },
  sourceChip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space(0.75),
    backgroundColor: theme.colors.surface,
    borderWidth: 1,
    borderColor: theme.colors.border,
    borderRadius: theme.radius.pill,
    paddingHorizontal: theme.space(1.25),
    paddingVertical: theme.space(0.75),
  },
  sourceLabel: { color: theme.colors.textMuted, fontSize: 13 },
  sourceCount: {
    color: theme.colors.accent,
    fontSize: 12,
    fontWeight: '700',
  },
  orb: {
    alignSelf: 'center',
    width: 168,
    height: 168,
    borderRadius: 84,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: theme.colors.surface,
    borderWidth: 2,
    borderColor: theme.colors.border,
    marginTop: theme.space(2),
  },
  orbActive: { borderColor: theme.colors.accent, backgroundColor: theme.colors.accentSoft },
  orbListening: { borderColor: theme.colors.success },
  orbIcon: { color: theme.colors.text, fontSize: 52 },
  state: { color: theme.colors.text, fontSize: 17, fontWeight: '600', textAlign: 'center' },
  partial: { color: theme.colors.accent, fontSize: 15, textAlign: 'center', fontStyle: 'italic' },
  error: { color: theme.colors.danger, fontSize: 13 },
  typeRow: { flexDirection: 'row', gap: theme.space(1) },
  question: { color: theme.colors.textMuted, fontSize: 14 },
  answer: { color: theme.colors.text, fontSize: 16, lineHeight: 23 },
  effect: { color: theme.colors.success, fontSize: 13 },
  steps: { color: theme.colors.textMuted, fontSize: 11 },
  noticeTitle: { color: theme.colors.warning, fontSize: 14, fontWeight: '700' },
  exampleTitle: { color: theme.colors.textMuted, fontSize: 12, textTransform: 'uppercase', letterSpacing: 1 },
  example: { color: theme.colors.text, fontSize: 15, lineHeight: 22 },
});
