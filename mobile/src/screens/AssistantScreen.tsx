import React, { useCallback, useEffect, useState } from 'react';
import {
  KeyboardAvoidingView,
  Platform,
  Pressable,
  RefreshControl,
  ScrollView,
  StyleSheet,
  TextInput,
  View,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useKeepAwake } from 'expo-keep-awake';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Banner, Card, Chip, SectionLabel, Txt } from '../components/ui';
import { Glass } from '../components/glass';
import { Orb } from '../components/Orb';
import { loadUrgent, Urgences } from '../components/Urgences';
import { AFaire } from '../components/AFaire';
import { loadTodos } from '../lib/todos';
import { tabBarSpace } from '../components/TabBar';
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
  const [refreshing, setRefreshing] = useState(false);
  const insets = useSafeAreaInsets();

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
  }, [listening, refreshing]);

  const active = state !== 'off';

  // Tirer vers le bas force la régénération de la liste — le seul geste qui
  // redemande le modèle. Le reste du temps le serveur rend son cache tant que
  // rien de neuf n'est arrivé.
  const refresh = useCallback(() => {
    setRefreshing(true);
    void Promise.all([loadUrgent(true), loadTodos(true)]).finally(() => setRefreshing(false));
  }, []);

  // Arrivée par le widget : on allume l'écoute et on saute le mot
  // d'activation — l'appui sur le widget en tient lieu.
  useEffect(() => {
    if (listenRequest === 0 || !voiceAvailable) return;
    void startConversation();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listenRequest]);

  const send = () => {
    const text = draft.trim();
    if (!text) return;
    setDraft('');
    void askText(text);
  };

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      keyboardVerticalOffset={theme.space.sm}
    >
      <ScrollView
        style={styles.flex}
        contentContainerStyle={styles.content}
        keyboardShouldPersistTaps="handled"
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={theme.colors.primary} />
        }
      >
        <View style={styles.header}>
          <View style={styles.brandRow}>
            <Glass radius={theme.radius.md} tone={theme.colors.primary} style={styles.mark}>
              <Feather name="cpu" size={16} color={theme.colors.primary} />
            </Glass>
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

        {/* Ce qu'il te reste à faire, avant tout le reste de l'écran. C'est la
            raison d'ouvrir l'app quand on ne vient pas lui parler.

            Deux listes, et l'ordre n'est pas indifférent : ce que tu t'es
            engagé à faire aujourd'hui passe devant ce qui vient d'arriver dans
            ta boîte. Un message peut attendre demain, une tâche datée non. */}
        <AFaire />
        <Urgences />

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
                style={({ pressed }) => pressed && styles.pressed}
              >
                <Glass radius={theme.radius.md} variant="subtle" style={styles.example}>
                  <Txt variant="small" style={styles.flex}>
                    {e}
                  </Txt>
                  <Feather name="arrow-up-right" size={15} color={theme.colors.textFaint} />
                </Glass>
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

      {/* Le composeur flotte lui aussi, juste au-dessus de la barre d'onglets :
          il appartient à la couche des commandes, pas à celle du contenu. */}
      <View style={[styles.composerWrap, { marginBottom: tabBarSpace(insets.bottom) }]}>
        <Glass variant="chrome" radius={theme.radius.xl} style={styles.composer}>
          <Feather name="edit-3" size={16} color={theme.colors.textFaint} />
          <TextInput
            value={draft}
            onChangeText={setDraft}
            onSubmitEditing={send}
            placeholder="Écris ta demande"
            placeholderTextColor={theme.colors.textFaint}
            returnKeyType="send"
            accessibilityLabel="Écrire une demande à Raoul"
            style={styles.composerInput}
          />
          {draft.trim() ? (
            <Pressable
              onPress={send}
              accessibilityRole="button"
              accessibilityLabel="Envoyer la demande"
              style={({ pressed }) => [styles.send, pressed && styles.pressed]}
            >
              <Feather name="arrow-up" size={16} color={theme.colors.onPrimary} />
            </Pressable>
          ) : null}
        </Glass>
      </View>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  content: {
    paddingHorizontal: theme.space.lg,
    paddingTop: theme.space.md,
    paddingBottom: theme.space.xl,
    gap: theme.space.lg,
  },
  header: { gap: theme.space.md },
  brandRow: { flexDirection: 'row', alignItems: 'center', gap: theme.space.md },
  mark: {
    width: 34,
    height: 34,
    alignItems: 'center',
    justifyContent: 'center',
  },
  chips: { flexDirection: 'row', flexWrap: 'wrap', gap: theme.space.sm },

  statusBlock: { gap: theme.space.xs, marginTop: -theme.space.lg },
  centered: { textAlign: 'center' },
  partial: { fontStyle: 'italic' },

  examples: { gap: theme.space.sm },
  example: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    minHeight: theme.touchMin,
    paddingHorizontal: theme.space.lg,
    paddingVertical: theme.space.md,
  },
  pressed: { opacity: 0.7 },

  thread: { gap: theme.space.md },
  effect: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },

  composerWrap: { paddingHorizontal: theme.space.lg },
  composer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    paddingHorizontal: theme.space.lg,
    paddingVertical: theme.space.xs,
  },
  composerInput: {
    flex: 1,
    minHeight: theme.touchMin,
    color: theme.colors.text,
    fontFamily: theme.type.body.font,
    fontSize: theme.type.body.fontSize,
  },
  send: {
    width: 32,
    height: 32,
    borderRadius: 16,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: theme.colors.primary,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.5,
    shadowRadius: 10,
    shadowOffset: { width: 0, height: 3 },
  },
});
