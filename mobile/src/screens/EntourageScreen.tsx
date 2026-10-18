import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Alert, Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Badge, Button, Chip, Empty, Field, IconButton, IconName, Notice, Panel, ScreenHeader, Section, Skeleton, T, Tone } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { Echange, Entourage, Importance, Personne, PersonneEdit } from '../api';
import { useSlot } from '../lib/cache';
import { ajouterPersonne, ENTOURAGE_KEY, loadEntourage, modifierPersonne, oublierFiche } from '../lib/entourage';

/**
 * Entourage : les gens avec qui il travaille, tels que Raoul les connaît.
 *
 * Raoul les apprend à la voix — il demande qui est quelqu'un qu'il ne connaît
 * pas, et retient la réponse — et s'en sert pour trier : un mail du patron
 * passe devant une relance de fournisseur. Ce qui pèse autant sur ce qu'il dit
 * doit se voir et se corriger ici : la place de chacun, son importance, les
 * adresses qui permettent de le reconnaître. En dessous, ce que Raoul a lu
 * récemment et ce qu'il en a compris, puis le reste de ce qu'il retient.
 */

const LEVELS: readonly { key: Importance; label: string; tone: Tone }[] = [
  { key: 'critique', label: 'Critique', tone: 'danger' },
  { key: 'haute', label: 'Haute', tone: 'warn' },
  { key: 'normale', label: 'Normale', tone: 'muted' },
  { key: 'basse', label: 'Basse', tone: 'faint' },
];

const RANK: Record<string, number> = { critique: 0, haute: 1, normale: 2, basse: 3 };

function levelOf(p: Personne) {
  return LEVELS.find((l) => l.key === p.importance);
}

export function EntourageScreen() {
  const insets = useSafeAreaInsets();
  const slot = useSlot<Entourage>(ENTOURAGE_KEY);
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);

  // Toujours relu à l'ouverture : Raoul a pu apprendre quelqu'un à la voix
  // depuis la dernière visite.
  useEffect(() => {
    void loadEntourage(true);
  }, []);

  const refresh = useCallback(() => {
    setBusy(true);
    void loadEntourage(true).finally(() => setBusy(false));
  }, []);

  const data = slot.data;
  const people = useMemo(
    () =>
      [...(data?.personnes ?? [])].sort(
        (a, b) => (RANK[a.importance ?? ''] ?? 2.5) - (RANK[b.importance ?? ''] ?? 2.5) || (a.subject ?? '').localeCompare(b.subject ?? '', 'fr'),
      ),
    [data],
  );

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      refreshControl={<RefreshControl refreshing={busy} onRefresh={refresh} tintColor={tokens.colors.pro} />}
      keyboardShouldPersistTaps="handled"
    >
      <ScreenHeader
        title="Entourage"
        subtitle="les gens avec qui tu travailles"
        right={adding ? null : <IconButton icon="user-plus" label="Ajouter quelqu’un" tone="default" onPress={() => setAdding(true)} />}
      />

      {slot.error ? (
        <Notice tone="warn" icon="wifi-off">
          <T v="small" tone="muted">
            {slot.error}
          </T>
        </Notice>
      ) : null}
      {!data && !slot.error ? <Skeleton lines={4} style={styles.skeleton} /> : null}

      {adding ? (
        <Panel>
          <T v="h">Ajouter quelqu’un</T>
          <PersonEditor onDone={() => setAdding(false)} />
        </Panel>
      ) : null}

      {data ? (
        <Section title="Les gens" count={people.length}>
          {people.length === 0 ? (
            <Empty
              icon="users"
              title="Personne pour l’instant"
              message="Quand Raoul croise quelqu’un qu’il ne connaît pas, il te demande qui c’est et le retient. Tu peux aussi ajouter les gens ici."
            />
          ) : null}
          {people.map((p) => (
            <PersonCard key={p.id} p={p} />
          ))}
        </Section>
      ) : null}

      {data && data.echanges.length > 0 ? (
        <Section title="Ce que Raoul a lu" count={data.echanges.length}>
          <Panel padded={false}>
            {data.echanges.map((e, i) => (
              <ExchangeRow key={e.id} e={e} first={i === 0} />
            ))}
          </Panel>
        </Section>
      ) : null}

      {data && data.autres.length > 0 ? (
        <Section title="Ce qu’il retient d’autre" count={data.autres.length}>
          <Panel padded={false}>
            {data.autres.map((f, i) => (
              <OtherFact key={f.id} f={f} first={i === 0} />
            ))}
          </Panel>
        </Section>
      ) : null}
    </ScrollView>
  );
}

function PersonCard({ p }: { p: Personne }) {
  const [open, setOpen] = useState(false);
  const level = levelOf(p);
  const color = level ? (level.tone === 'faint' || level.tone === 'muted' ? tokens.colors.text2 : level.tone === 'danger' ? tokens.colors.danger : tokens.colors.warn) : tokens.colors.text3;
  const name = p.subject || 'Sans nom';
  return (
    <Panel style={level?.key === 'critique' ? { borderColor: alpha(tokens.colors.danger, 0.45) } : undefined}>
      <Pressable
        onPress={() => setOpen((v) => !v)}
        accessibilityRole="button"
        accessibilityLabel={`${name}${p.role ? `, ${p.role}` : ''}`}
        accessibilityState={{ expanded: open }}
        style={({ pressed }) => [styles.personHead, pressed && styles.pressed]}
      >
        <View style={[styles.avatar, { borderColor: alpha(color, 0.6), backgroundColor: alpha(color, 0.1) }]}>
          <T v="h" style={{ color }}>
            {name.slice(0, 1).toUpperCase()}
          </T>
        </View>
        <View style={styles.flex}>
          <T v="bodyStrong" numberOfLines={1}>
            {name}
          </T>
          <T v="small" tone="muted" numberOfLines={2}>
            {p.role || p.content}
          </T>
        </View>
        {level ? <Badge label={level.label} tone={level.tone} /> : <Badge label="à situer" tone="faint" />}
      </Pressable>
      {!open && (p.adresses?.length || p.slack_ids?.length) ? (
        <View style={styles.ids}>
          {p.adresses?.map((a) => (
            <Chip key={a} label={a} icon="mail" />
          ))}
          {p.slack_ids?.length ? <Chip label={`Slack${p.slack_ids.length > 1 ? ` ×${p.slack_ids.length}` : ''}`} icon="hash" /> : null}
        </View>
      ) : null}
      {open ? (
        <>
          {p.role && p.content && p.content !== p.role ? (
            <T v="small" tone="muted" selectable>
              {p.content}
            </T>
          ) : null}
          <PersonEditor p={p} onDone={() => setOpen(false)} />
        </>
      ) : null}
    </Panel>
  );
}

function splitList(s: string): string[] {
  return s
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/** Le formulaire d'une personne : ajout quand p est absent, correction sinon. */
function PersonEditor({ p, onDone }: { p?: Personne; onDone: () => void }) {
  const [subject, setSubject] = useState(p?.subject ?? '');
  const [role, setRole] = useState(p?.role ?? '');
  const [importance, setImportance] = useState<Importance | ''>(p?.importance ?? '');
  const [adresses, setAdresses] = useState((p?.adresses ?? []).join(', '));
  const [content, setContent] = useState(p && p.content !== p.role ? p.content : '');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    if (!subject.trim()) {
      setError('Il faut au moins un prénom.');
      return;
    }
    setSaving(true);
    setError(null);
    const edit: PersonneEdit = {
      sujet: subject.trim(),
      contenu: content.trim() || role.trim(),
      role: role.trim(),
      importance,
      adresses: splitList(adresses),
    };
    try {
      if (p) await modifierPersonne(p.id, edit);
      else await ajouterPersonne(edit);
      onDone();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const forget = () => {
    if (!p) return;
    Alert.alert('Oublier cette personne ?', `Raoul ne saura plus qui est ${p.subject || 'cette personne'}.`, [
      { text: 'Annuler', style: 'cancel' },
      {
        text: 'Oublier',
        style: 'destructive',
        onPress: () => void oublierFiche(p.id).catch((err) => Alert.alert('Échec', (err as Error).message)),
      },
    ]);
  };

  return (
    <View style={styles.editor}>
      <Field label="Prénom" value={subject} onChangeText={setSubject} autoCapitalize="words" placeholder="Cyril" />
      <Field label="Qui c’est pour toi" value={role} onChangeText={setRole} autoCapitalize="sentences" placeholder="mon patron, CEO de PXCom" />
      <View style={styles.levels}>
        <T v="label" tone="muted" style={styles.levelsLabel}>
          IMPORTANCE
        </T>
        <View style={styles.levelChips}>
          {LEVELS.map((l) => (
            <Chip key={l.key} label={l.label} tone={l.tone} active={importance === l.key} onPress={() => setImportance(importance === l.key ? '' : l.key)} />
          ))}
        </View>
      </View>
      <Field
        label="Adresses mail"
        value={adresses}
        onChangeText={setAdresses}
        keyboardType="email-address"
        placeholder="cyril@pxcom.aero"
        hint="Séparées par des virgules. C’est par elles que Raoul le reconnaît sans le confondre avec un homonyme."
      />
      <Field
        label="Ce qu’il faut savoir"
        value={content}
        onChangeText={setContent}
        autoCapitalize="sentences"
        autoCorrect
        multiline
        style={styles.multiline}
        placeholder="Porte le dossier des boxes, on se tutoie"
      />
      {error ? (
        <T v="mono" tone="danger">
          {error}
        </T>
      ) : null}
      <View style={styles.actions}>
        <View style={styles.flex}>
          <Button label={p ? 'Enregistrer' : 'Ajouter'} icon="check" compact loading={saving} onPress={() => void save()} />
        </View>
        <Button label="Annuler" variant="ghost" compact onPress={onDone} />
        {p ? <IconButton icon="trash-2" label="Oublier cette personne" tone="danger" onPress={forget} /> : null}
      </View>
    </View>
  );
}

const SOURCE_ICON: Record<Echange['source'], IconName> = { mail: 'mail', slack: 'hash', whatsapp: 'message-circle' };

function ExchangeRow({ e, first }: { e: Echange; first: boolean }) {
  const [open, setOpen] = useState(false);
  const last = e.dernier_de ? `${e.dernier_de_toi ? 'toi' : e.dernier_de}${e.dernier_quand ? ` · ${ago(e.dernier_quand)}` : ''}` : '';
  return (
    <View style={[styles.row, !first && styles.rowBorder]}>
      <Pressable
        onPress={() => setOpen((v) => !v)}
        accessibilityRole="button"
        accessibilityLabel={e.sujet}
        accessibilityState={{ expanded: open }}
        style={({ pressed }) => [styles.rowHead, pressed && styles.pressed]}
      >
        <Feather name={SOURCE_ICON[e.source] ?? 'message-square'} size={14} color={e.resume ? tokens.colors.pro : tokens.colors.text3} />
        <View style={styles.flex}>
          <T v="bodyStrong" numberOfLines={1}>
            {e.sujet || '(sans objet)'}
          </T>
          <T v="mono" tone="faint" numberOfLines={1}>
            {last ? `dernier : ${last} · ` : ''}lu {ago(e.vu_le)}
          </T>
        </View>
        <Feather name={open ? 'chevron-up' : 'chevron-down'} size={14} color={tokens.colors.text3} />
      </Pressable>
      {open ? (
        <View style={styles.rowBody}>
          {e.pour_toi ? <Badge label={e.pour_toi} tone="muted" /> : null}
          {e.resume ? (
            <>
              <T v="label" tone="muted">
                CE QU’IL EN A COMPRIS
              </T>
              <T v="small" tone="muted" selectable>
                {e.resume}
              </T>
            </>
          ) : null}
          {e.dernier_extrait ? (
            <>
              <T v="label" tone="muted">
                DERNIER MESSAGE
              </T>
              <T v="small" tone="faint" numberOfLines={8} selectable>
                {e.dernier_extrait}
              </T>
            </>
          ) : null}
        </View>
      ) : null}
    </View>
  );
}

const KIND_LABEL: Record<string, string> = { projet: 'dossier', preference: 'habitude', fait: 'fait' };

function OtherFact({ f, first }: { f: Personne; first: boolean }) {
  const forget = () =>
    Alert.alert('Oublier cette fiche ?', f.subject ? `Raoul oubliera « ${f.subject} ».` : 'Raoul oubliera cette information.', [
      { text: 'Annuler', style: 'cancel' },
      { text: 'Oublier', style: 'destructive', onPress: () => void oublierFiche(f.id).catch((err) => Alert.alert('Échec', (err as Error).message)) },
    ]);
  return (
    <View style={[styles.row, styles.rowHead, !first && styles.rowBorder]}>
      <View style={styles.flex}>
        <T v="mono" tone="faint">
          {(KIND_LABEL[f.kind] ?? f.kind).toUpperCase()}
          {f.subject ? ` · ${f.subject}` : ''}
        </T>
        <T v="small" tone="muted" selectable>
          {f.content}
        </T>
      </View>
      <IconButton icon="trash-2" label="Oublier cette fiche" tone="muted" onPress={forget} />
    </View>
  );
}

/** « il y a 3 min », « hier », « le 12/10 » — l'écart compte plus que l'heure. */
function ago(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const min = Math.round((Date.now() - t) / 60000);
  if (min < 1) return 'à l’instant';
  if (min < 60) return `il y a ${min} min`;
  const h = Math.round(min / 60);
  if (h < 24) return `il y a ${h} h`;
  const d = Math.round(h / 24);
  if (d === 1) return 'hier';
  if (d < 7) return `il y a ${d} jours`;
  const date = new Date(t);
  return `le ${String(date.getDate()).padStart(2, '0')}/${String(date.getMonth() + 1).padStart(2, '0')}`;
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  pressed: { opacity: 0.65 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xl },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  personHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md },
  avatar: { width: 40, height: 40, borderRadius: 20, borderWidth: 1, alignItems: 'center', justifyContent: 'center' },
  ids: { flexDirection: 'row', flexWrap: 'wrap', gap: tokens.space.xs },
  editor: { gap: tokens.space.md, marginTop: tokens.space.sm },
  levels: { gap: tokens.space.xs },
  levelsLabel: { textTransform: 'uppercase' },
  levelChips: { flexDirection: 'row', flexWrap: 'wrap', gap: tokens.space.xs },
  multiline: { minHeight: 72, textAlignVertical: 'top' },
  actions: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  row: { paddingHorizontal: tokens.space.lg },
  rowBorder: { borderTopWidth: 1, borderTopColor: tokens.colors.line },
  rowHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, minHeight: tokens.touch + 8, paddingVertical: tokens.space.sm },
  rowBody: { paddingBottom: tokens.space.md, gap: tokens.space.sm, alignItems: 'flex-start' },
});
