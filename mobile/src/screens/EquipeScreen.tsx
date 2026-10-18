import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Alert, Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Badge, Button, Empty, IconButton, Notice, Panel, ScreenHeader, Section, Skeleton, T } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { api, Connaissance, EquipeInfo, Membre } from '../api';
import { useSlot } from '../lib/cache';
import { CONNAISSANCES_KEY, EQUIPE_KEY, loadConnaissances, loadEquipe, oublierConnaissance } from '../lib/equipe';
import { speak, speakOnDevice, stopSpeaking } from '../lib/speech';

/**
 * Équipe : qui suit tes projets perso, et ce qu'ils en savent.
 *
 * En haut, l'organigramme — Raoul, puis la responsable informatique, puis
 * l'ingénieur —, chacun avec sa couleur, sa voix, et un bouton pour lui
 * parler directement. En bas, la mémoire de l'équipe : les fiches qu'elle a
 * reçues au départ et celles qu'elle a écrites en travaillant. C'est là qu'on
 * voit ce que l'ingénieur a retenu, et qu'on retire une fiche devenue fausse.
 */
export function EquipeScreen({ onTalk }: { onTalk: (agent: string) => void }) {
  const insets = useSafeAreaInsets();
  const equipeSlot = useSlot<EquipeInfo | false>(EQUIPE_KEY);
  const notesSlot = useSlot<Connaissance[] | false>(CONNAISSANCES_KEY);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void loadEquipe();
    void loadConnaissances();
    return () => stopSpeaking();
  }, []);

  const refresh = useCallback(() => {
    setBusy(true);
    void Promise.all([loadEquipe(true), loadConnaissances(true)]).finally(() => setBusy(false));
  }, []);

  const equipe = equipeSlot.data || null;

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      refreshControl={<RefreshControl refreshing={busy} onRefresh={refresh} tintColor={tokens.colors.perso} />}
    >
      <ScreenHeader title="Équipe" subtitle="ceux qui suivent tes projets perso" />

      {equipeSlot.data === false ? (
        <Empty icon="users" title="Pas d’équipe sur ce serveur" message="L’équipe technique arrive avec le suivi des projets (PROJETS_REGISTRE)." />
      ) : null}
      {equipeSlot.error ? (
        <Notice tone="warn" icon="wifi-off">
          <T v="small" tone="muted">
            {equipeSlot.error}
          </T>
        </Notice>
      ) : null}
      {!equipe && equipeSlot.data !== false && !equipeSlot.error ? <Skeleton lines={4} style={styles.skeleton} /> : null}

      {equipe ? (
        <Section title="L’organigramme">
          {ordered(equipe.membres).map(({ m, depth }) => (
            <MemberCard key={m.id} m={m} depth={depth} speaking={equipe.interlocuteur === m.id} onTalk={() => onTalk(m.id)} />
          ))}
        </Section>
      ) : null}

      {equipe ? <Savoir slot={notesSlot} /> : null}
    </ScrollView>
  );
}

/** L'organigramme se lit du sommet vers le terrain : chacun sous son supérieur. */
function ordered(members: Membre[]): { m: Membre; depth: number }[] {
  const out: { m: Membre; depth: number }[] = [];
  const walk = (parent: string | undefined, depth: number) => {
    for (const m of members.filter((x) => (x.superieur ?? '') === (parent ?? ''))) {
      out.push({ m, depth });
      walk(m.id, depth + 1);
    }
  };
  walk(undefined, 0);
  // Ceux qui n'ont pas trouvé leur place (supérieur inconnu) restent visibles.
  for (const m of members) if (!out.some((o) => o.m.id === m.id)) out.push({ m, depth: 0 });
  return out;
}

function MemberCard({ m, depth, speaking, onTalk }: { m: Membre; depth: number; speaking: boolean; onTalk: () => void }) {
  const [playing, setPlaying] = useState(false);
  const listen = async () => {
    setPlaying(true);
    try {
      const { url, texte } = await api.voixMembre(m.id);
      await speak(texte, undefined, url, m.id);
    } catch {
      // Pas de voix serveur (voix gratuites du téléphone, ou quota épuisé) :
      // on l'entend quand même, avec la voix de l'iPhone qui lui est attribuée.
      await speakOnDevice(`Salut, c'est ${m.nom}, ${m.role.toLowerCase()}.`, undefined, m.id);
    } finally {
      setPlaying(false);
    }
  };
  return (
    <View style={[styles.member, { marginLeft: depth * tokens.space.lg }]}>
      {depth > 0 ? <View style={[styles.branch, { borderColor: alpha(m.couleur, 0.35) }]} /> : null}
      <Panel style={[styles.flex, speaking && { borderColor: alpha(m.couleur, 0.6) }]}>
        <View style={styles.memberHead}>
          <View style={[styles.avatar, { borderColor: alpha(m.couleur, 0.6), backgroundColor: alpha(m.couleur, 0.1) }]}>
            <T v="h" style={{ color: m.couleur }}>
              {m.nom.slice(0, 1).toUpperCase()}
            </T>
          </View>
          <View style={styles.flex}>
            <T v="h">{m.nom}</T>
            <T v="label" style={{ color: m.couleur, textTransform: 'uppercase' }}>
              {m.role}
            </T>
          </View>
          {speaking ? <Badge label="a la parole" tone="ok" /> : null}
        </View>
        <T v="small" tone="muted">
          {m.presentation}
        </T>
        <View style={styles.actions}>
          <View style={styles.flex}>
            <Button label={speaking ? `Reprendre avec ${m.nom}` : `Parler à ${m.nom}`} icon="message-square" compact onPress={onTalk} />
          </View>
          <IconButton icon={playing ? 'volume-2' : 'play-circle'} label={`Écouter la voix de ${m.nom}`} tone="muted" disabled={playing} onPress={() => void listen()} />
        </View>
      </Panel>
    </View>
  );
}

function Savoir({ slot }: { slot: { data: Connaissance[] | false | null; error: string | null; loading: boolean } }) {
  const notes = slot.data || [];
  const groups = useMemo(() => {
    const map = new Map<string, Connaissance[]>();
    for (const n of notes) map.set(n.projet, [...(map.get(n.projet) ?? []), n]);
    return [...map.entries()].sort(([a], [b]) => (a === 'general' ? -1 : b === 'general' ? 1 : a.localeCompare(b)));
  }, [notes]);

  return (
    <Section title="Ce que l’équipe sait" count={notes.length}>
      {slot.loading && !slot.data ? <Skeleton lines={3} style={styles.skeleton} /> : null}
      {slot.data && notes.length === 0 ? (
        <Empty icon="book-open" title="Rien de noté pour l’instant" message="L’ingénieur note ce qu’il comprend en travaillant : ses fiches apparaîtront ici." />
      ) : null}
      {groups.map(([projet, list]) => (
        <Panel key={projet} padded={false}>
          <View style={styles.groupHead}>
            <T v="label" tone="muted" style={{ textTransform: 'uppercase' }}>
              {projet === 'general' ? 'Général' : projet}
            </T>
            <T v="label" tone="faint">
              {list.length}
            </T>
          </View>
          {list.map((n) => (
            <Note key={n.id} n={n} />
          ))}
        </Panel>
      ))}
    </Section>
  );
}

function Note({ n }: { n: Connaissance }) {
  const [open, setOpen] = useState(false);
  const forget = () =>
    Alert.alert('Retirer cette fiche ?', `« ${n.sujet} » sera oubliée par toute l’équipe.`, [
      { text: 'Annuler', style: 'cancel' },
      { text: 'Retirer', style: 'destructive', onPress: () => void oublierConnaissance(n.id).catch((err) => Alert.alert('Échec', (err as Error).message)) },
    ]);
  const origin = n.origine === 'amorce' ? 'fiche de départ' : n.origine === 'equipe' ? `écrite par ${n.auteur === 'responsable_info' ? 'la responsable' : 'l’ingénieur'}` : 'saisie à la main';
  return (
    <View style={styles.note}>
      <Pressable onPress={() => setOpen((v) => !v)} accessibilityRole="button" accessibilityLabel={n.sujet} style={({ pressed }) => [styles.noteHead, pressed && styles.pressed]}>
        <Feather name={n.origine === 'equipe' ? 'edit-3' : 'bookmark'} size={13} color={n.origine === 'equipe' ? tokens.colors.perso : tokens.colors.text3} />
        <View style={styles.flex}>
          <T v="bodyStrong" numberOfLines={1}>
            {n.sujet}
          </T>
          <T v="mono" tone="faint">
            {origin} · {new Date(n.updated_at).toLocaleDateString('fr-FR')}
          </T>
        </View>
        <Feather name={open ? 'chevron-up' : 'chevron-down'} size={14} color={tokens.colors.text3} />
      </Pressable>
      {open ? (
        <View style={styles.noteBody}>
          <T v="small" tone="muted" selectable>
            {n.contenu}
          </T>
          <Button label="Retirer la fiche" variant="ghost" icon="trash-2" compact onPress={forget} />
        </View>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  pressed: { opacity: 0.65 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xl },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  member: { flexDirection: 'row' },
  branch: { width: tokens.space.md, marginTop: 28, borderTopWidth: 1, borderLeftWidth: 1, borderTopLeftRadius: 6, height: 16, marginRight: -1 },
  memberHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md },
  avatar: { width: 40, height: 40, borderRadius: 20, borderWidth: 1, alignItems: 'center', justifyContent: 'center' },
  actions: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  groupHead: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingHorizontal: tokens.space.lg,
    paddingTop: tokens.space.md,
    paddingBottom: tokens.space.xs,
  },
  note: { borderTopWidth: 1, borderTopColor: tokens.colors.line },
  noteHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, minHeight: tokens.touch + 8, paddingHorizontal: tokens.space.lg },
  noteBody: { paddingHorizontal: tokens.space.lg, paddingBottom: tokens.space.md, gap: tokens.space.sm },
});
