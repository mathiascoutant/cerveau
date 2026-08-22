import React, { useEffect, useState } from 'react';
import { KeyboardAvoidingView, Platform, Pressable, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useKeepAwake } from 'expo-keep-awake';

import { Banner, Card, Chip, EmptyState, SectionLabel, Txt } from '../components/ui';
import { Orb } from '../components/Orb';
import { useRaoul, RaoulState } from '../hooks/useRaoul';
import { api, SourceStatus } from '../api';
import { theme } from '../theme';

const STATE_LABEL: Record<RaoulState, string> = {
  off: 'Micro coupé',
  waiting: 'Dis « OK Raoul »',
  listening: 'Je t’écoute',
  thinking: 'Je consulte tes sources',
  speaking: 'Raoul répond',
};

const SOURCES: Record<string, { label: string; icon: React.ComponentProps<typeof Feather>['name'] }> = {
  gandi: { label: 'Mails', icon: 'mail' },
  slack: { label: 'Slack', icon: 'hash' },
  whatsapp: { label: 'WhatsApp', icon: 'message-circle' },
  calendar: { label: 'Agenda', icon: 'calendar' },
};

const EXAMPLES = [
  'Je peux aller faire du sport à 10h demain ?',
  'Est-ce que j’ai raté quelque chose d’urgent ?',
  'Prépare-moi une réponse au mail d’Olivier pour dire que c’est ok.',
  'Bloque-moi deux heures jeudi après-midi.',
];

type Props = {
  /**
   * Incrémenté à chaque appui sur le widget de l'écran d'accueil. Chaque
   * nouvelle valeur déclenche l'écoute — y compris si elle tourne déjà, auquel
   * cas on repart sur une demande neuve plutôt que de laisser l'appui sans effet.
   */
  listenRequest?: number;
};

export function AssistantScreen({ listenRequest = 0 }: Props) {
  const {
    state,
    partial,
    error,
    history,
    start,
    startConversation,
    stop,
    pushToTalk,
    askText,
    voiceAvailable,
    inConversation,
  } = useRaoul();
  const [sources, setSources] = useState<SourceStatus[]>([]);
  const [draft, setDraft] = useState('');

  useKeepAwake();

  const listening = state === 'waiting';
  useEffect(() => {
    let alive = true;
    api
      .status()
      .then((res) => alive && setSources(res.sources))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, [listening]);

  const active = state !== 'off';

  // Arrivée par le widget : on allume l'écoute et on saute le mot
  // d'activation — l'appui sur le widget en tient lieu.
  useEffect(() => {
    if (listenRequest === 0 || !voiceAvailable) return;
    void startConversation();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listenRequest]);

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      keyboardVerticalOffset={80}
    >
      <ScrollView
        style={styles.screen}
        contentContainerStyle={styles.content}
        keyboardShouldPersistTaps="handled"
      >
        <View style={styles.header}>
          <View style={styles.brandRow}>
            <View style={styles.mark}>
              <Feather name="cpu" size={16} color={theme.colors.primary} />
            </View>
            <Txt variant="title">Raoul</Txt>
          </View>
          {/* Seules les sources branchées s'affichent : la pastille grise
              d'un compte absent est encore une façon d'en parler. */}
          <View style={styles.chips}>
            {sources
              .filter((s) => s.connected)
              .map((s) => (
                <Chip
                  key={s.provider}
                  icon={SOURCES[s.provider]?.icon ?? 'circle'}
                  label={SOURCES[s.provider]?.label ?? s.provider}
                  count={s.unread}
                  state={s.error ? 'warn' : 'on'}
                />
              ))}
          </View>
        </View>

        <Orb
          state={state}
          enabled={voiceAvailable}
          onPress={() => (active ? stop() : start())}
          onLongPress={pushToTalk}
        />

        <View style={styles.statusBlock}>
          <Txt variant="heading" style={styles.centered}>
            {voiceAvailable ? STATE_LABEL[state] : 'Mode texte'}
          </Txt>
          {state === 'listening' && partial ? (
            <Txt variant="body" tone="primary" style={[styles.centered, styles.partial]}>
              « {partial} »
            </Txt>
          ) : inConversation && state === 'listening' ? (
            <Txt variant="small" tone="faint" style={styles.centered}>
              Conversation ouverte · dis « merci Raoul » pour la refermer
            </Txt>
          ) : voiceAvailable && !active ? (
            <Txt variant="small" tone="faint" style={styles.centered}>
              Touche pour l’écoute permanente · appui long pour parler tout de suite
            </Txt>
          ) : null}
        </View>

        {!voiceAvailable && (
          <Banner tone="warning" icon="smartphone">
            <Txt variant="bodyStrong" tone="warning">
              Expo Go — aperçu de l’interface
            </Txt>
            <Txt variant="small" tone="muted">
              « OK Raoul » et l’agenda reposent sur des modules natifs absents d’Expo Go. Écris ta
              demande ci-dessous : Raoul répond et lit sa réponse à voix haute.
            </Txt>
          </Banner>
        )}

        {error && (
          <Banner tone="danger" icon="alert-triangle">
            <Txt variant="small" tone="danger">
              {error}
            </Txt>
          </Banner>
        )}

        {history.length === 0 ? (
          <View style={styles.examples}>
            <SectionLabel>Essaie</SectionLabel>
            {EXAMPLES.map((e) => (
              <Pressable
                key={e}
                onPress={() => void askText(e)}
                accessibilityRole="button"
                accessibilityLabel={`Demander : ${e}`}
                style={({ pressed }) => [styles.example, pressed && styles.examplePressed]}
              >
                <Txt variant="small" style={styles.flex}>
                  {e}
                </Txt>
                <Feather name="arrow-up-right" size={15} color={theme.colors.textFaint} />
              </Pressable>
            ))}
          </View>
        ) : (
          <View style={styles.thread}>
            <SectionLabel>Échanges</SectionLabel>
            {history.map((item) => (
              <Card key={item.id}>
                <Txt variant="small" tone="faint">
                  {item.question}
                </Txt>
                <Txt variant="body">{item.answer}</Txt>
                {item.effects?.map((effect) => (
                  <View key={effect} style={styles.effect}>
                    <Feather name="check-circle" size={13} color={theme.colors.success} />
                    <Txt variant="mono" tone="success" style={styles.flex}>
                      {effect}
                    </Txt>
                  </View>
                ))}
                {item.steps?.length ? (
                  <Txt variant="mono" tone="faint">
                    Consulté : {[...new Set(item.steps)].join(' · ')}
                  </Txt>
                ) : null}
              </Card>
            ))}
          </View>
        )}
      </ScrollView>

      <View style={styles.composer}>
        <Feather name="edit-3" size={16} color={theme.colors.textFaint} />
        <TextInputBox
          value={draft}
          onChange={setDraft}
          onSubmit={() => {
            const text = draft.trim();
            if (!text) return;
            setDraft('');
            void askText(text);
          }}
        />
      </View>
    </KeyboardAvoidingView>
  );
}

/** Champ de saisie du composeur, séparé pour garder l'écran lisible. */
function TextInputBox({
  value,
  onChange,
  onSubmit,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
}) {
  const { TextInput } = require('react-native') as typeof import('react-native');
  return (
    <TextInput
      value={value}
      onChangeText={onChange}
      onSubmitEditing={onSubmit}
      placeholder="Écris ta demande"
      placeholderTextColor={theme.colors.textFaint}
      returnKeyType="send"
      accessibilityLabel="Écrire une demande à Raoul"
      style={styles.composerInput}
    />
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  screen: { flex: 1, backgroundColor: theme.colors.background },
  content: {
    paddingHorizontal: theme.space.xl,
    paddingTop: theme.space.md,
    paddingBottom: theme.space.xl,
    gap: theme.space.lg,
  },
  header: { gap: theme.space.md },
  brandRow: { flexDirection: 'row', alignItems: 'center', gap: theme.space.md },
  mark: {
    width: 32,
    height: 32,
    borderRadius: theme.radius.sm,
    backgroundColor: `${theme.colors.primary}18`,
    alignItems: 'center',
    justifyContent: 'center',
  },
  chips: { flexDirection: 'row', flexWrap: 'wrap', gap: theme.space.sm },

  statusBlock: { gap: theme.space.xs, marginTop: -theme.space.md },
  centered: { textAlign: 'center' },
  partial: { fontStyle: 'italic' },

  examples: { gap: theme.space.sm },
  example: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    minHeight: theme.touchMin,
    backgroundColor: theme.colors.surface,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.border,
    borderRadius: theme.radius.md,
    paddingHorizontal: theme.space.lg,
    paddingVertical: theme.space.md,
  },
  examplePressed: { backgroundColor: theme.colors.surfaceActive },

  thread: { gap: theme.space.md },
  effect: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },

  composer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    paddingHorizontal: theme.space.xl,
    paddingVertical: theme.space.md,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: theme.colors.border,
    backgroundColor: theme.colors.surface,
  },
  composerInput: {
    flex: 1,
    minHeight: theme.touchMin,
    color: theme.colors.text,
    fontFamily: theme.type.body.font,
    fontSize: theme.type.body.fontSize,
  },
});
