import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Pressable,
  RefreshControl,
  ScrollView,
  StyleSheet,
  View,
} from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { Feather } from '@expo/vector-icons';

import { Banner, Card, Divider, EmptyState, ScreenHeader, SectionLabel, Txt } from '../components/ui';
import { api, EmailDraft } from '../api';
import { speak, stopSpeaking } from '../lib/speech';
import { theme } from '../theme';

/**
 * Les réponses de mail que Raoul a rédigées.
 *
 * Rien ne part d'ici, et c'est le principe : Raoul tient la plume, pas le
 * bouton « envoyer ». On copie le mail, on l'ouvre dans son client, on
 * l'expédie soi-même — un mail parti par erreur ne se rattrape pas.
 */
export function DraftsScreen() {
  const [drafts, setDrafts] = useState<EmailDraft[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const [reading, setReading] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const res = await api.drafts();
      setDrafts(res.drafts);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    void load();
    return () => stopSpeaking();
  }, [load]);

  const copy = async (draft: EmailDraft) => {
    await Clipboard.setStringAsync(draft.body);
    setCopied(draft.id);
    setTimeout(() => setCopied((id) => (id === draft.id ? null : id)), 2000);
  };

  /** Relire le mail sans repasser par la voix : utile pour le valider. */
  const read = async (draft: EmailDraft) => {
    if (reading === draft.id) {
      stopSpeaking();
      setReading(null);
      return;
    }
    stopSpeaking();
    setReading(draft.id);
    await speak(draft.body, draft.language);
    setReading((id) => (id === draft.id ? null : id));
  };

  const remove = (draft: EmailDraft) => {
    Alert.alert(
      'Supprimer cette réponse ?',
      `La réponse pour ${draft.to} sera perdue.`,
      [
        { text: 'Annuler', style: 'cancel' },
        {
          text: 'Supprimer',
          style: 'destructive',
          onPress: () => {
            setDrafts((prev) => prev.filter((d) => d.id !== draft.id));
            api.deleteDraft(draft.id).catch((err: Error) => {
              setError(err.message);
              void load();
            });
          },
        },
      ],
    );
  };

  if (loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator color={theme.colors.primary} size="large" />
      </View>
    );
  }

  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={styles.content}
      refreshControl={
        <RefreshControl
          refreshing={busy}
          onRefresh={() => {
            setBusy(true);
            void load();
          }}
          tintColor={theme.colors.primary}
        />
      }
    >
      <ScreenHeader
        title="Réponses"
        subtitle="Rédigées par Raoul, à copier et envoyer toi-même"
      />

      {error && (
        <Banner tone="danger" icon="alert-triangle">
          <Txt variant="small" tone="danger">
            {error}
          </Txt>
        </Banner>
      )}

      {drafts.length === 0 ? (
        <Card>
          <EmptyState
            icon="edit-3"
            title="Aucune réponse préparée"
            message="Dis à Raoul « prépare-moi une réponse à ce mail » et elle apparaîtra ici, prête à copier."
          />
        </Card>
      ) : (
        drafts.map((draft) => (
          <Card key={draft.id}>
            <View style={styles.head}>
              <View style={styles.flex}>
                <SectionLabel>Pour {draft.to}</SectionLabel>
                <Txt variant="bodyStrong" numberOfLines={2}>
                  {draft.subject || '(sans objet)'}
                </Txt>
              </View>
              {/* La langue ne s'affiche que si ce n'est pas du français :
                  c'est l'information qui surprend, pas celle qui va de soi. */}
              {draft.language && draft.language !== 'fr' ? (
                <View style={styles.langTag}>
                  <Txt variant="mono" tone="primary">
                    {draft.language.toUpperCase()}
                  </Txt>
                </View>
              ) : null}
            </View>

            <Txt variant="body" style={styles.body}>
              {draft.body}
            </Txt>

            <Txt variant="mono" tone="faint">
              {draft.to_addr ? `${draft.to_addr} · ` : ''}
              {formatWhen(draft.updated_at)}
            </Txt>

            <Divider />

            <View style={styles.actions}>
              <Action
                icon={copied === draft.id ? 'check' : 'copy'}
                label={copied === draft.id ? 'Copié' : 'Copier'}
                tone={copied === draft.id ? 'success' : 'primary'}
                onPress={() => void copy(draft)}
              />
              <Action
                icon={reading === draft.id ? 'square' : 'volume-2'}
                label={reading === draft.id ? 'Stop' : 'Écouter'}
                onPress={() => void read(draft)}
              />
              <Action icon="trash-2" label="Supprimer" tone="danger" onPress={() => remove(draft)} />
            </View>
          </Card>
        ))
      )}
    </ScrollView>
  );
}

function Action({
  icon,
  label,
  onPress,
  tone = 'muted',
}: {
  icon: React.ComponentProps<typeof Feather>['name'];
  label: string;
  onPress: () => void;
  tone?: 'primary' | 'success' | 'danger' | 'muted';
}) {
  const color =
    tone === 'primary'
      ? theme.colors.primary
      : tone === 'success'
        ? theme.colors.success
        : tone === 'danger'
          ? theme.colors.danger
          : theme.colors.textMuted;
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label}
      style={({ pressed }) => [styles.action, pressed && styles.actionPressed]}
    >
      <Feather name={icon} size={15} color={color} />
      <Txt variant="small" style={{ color }}>
        {label}
      </Txt>
    </Pressable>
  );
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  const time = d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
  return sameDay ? `aujourd’hui à ${time}` : `${d.toLocaleDateString('fr-FR')} à ${time}`;
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  screen: { flex: 1, backgroundColor: theme.colors.background },
  content: {
    paddingHorizontal: theme.space.xl,
    paddingTop: theme.space.md,
    paddingBottom: theme.space.xxxl,
    gap: theme.space.lg,
  },
  center: {
    flex: 1,
    backgroundColor: theme.colors.background,
    alignItems: 'center',
    justifyContent: 'center',
  },
  head: { flexDirection: 'row', alignItems: 'flex-start', gap: theme.space.sm },
  langTag: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: `${theme.colors.primary}55`,
    borderRadius: theme.radius.pill,
    paddingHorizontal: theme.space.sm,
    paddingVertical: 2,
  },
  // Le mail se lit en entier : c'est ce qu'on va copier, on doit pouvoir le
  // relire avant de l'envoyer.
  body: { marginTop: theme.space.xs },
  actions: { flexDirection: 'row', gap: theme.space.sm },
  action: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.space.sm,
    minHeight: theme.touchMin,
    borderRadius: theme.radius.sm,
    backgroundColor: theme.colors.surfaceActive,
  },
  actionPressed: { opacity: 0.6 },
});
