import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Linking, Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Badge, Button, Chip, Divider, Empty, Field, Notice, Panel, Readout, ScreenHeader, Skeleton, T, Tone } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { tokens } from '../design/tokens';
import { api, ApiError, CSPComment, CSPList, CSPTicket } from '../api';

type Filter = 'ouverts' | 'tous' | 'moi';

/**
 * Les tickets CSP, tels que Tuleap les rend.
 *
 * Rien ici n'est inventé : la liste vient du tracker configuré sur le serveur,
 * avec les droits de la clé saisie dans Accès. Un ticket se lit ici, se
 * détaille ici, et s'ouvre dans Tuleap d'un geste quand il faut y écrire.
 */
export function CSPScreen({ onBack, onOpen, onAccess }: { onBack: () => void; onOpen: (t: CSPTicket) => void; onAccess: () => void }) {
  const insets = useSafeAreaInsets();
  const [data, setData] = useState<CSPList | null>(null);
  const [error, setError] = useState<{ message: string; status?: number } | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<Filter>('ouverts');

  const load = useCallback(async (refresh: boolean) => {
    setError(null);
    try {
      const res = await api.cspTickets(refresh);
      setData(res);
    } catch (err) {
      const e = err as Error;
      setError({ message: e.message, status: err instanceof ApiError ? err.status : undefined });
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void load(false);
  }, [load]);

  const tickets = useMemo(() => {
    const list = data?.tickets ?? [];
    const q = query.trim().toLowerCase();
    const me = data?.scope.user?.toLowerCase() ?? '';
    return list.filter((t) => {
      if (filter === 'ouverts' && t.ferme) return false;
      if (filter === 'moi' && (!me || !(t.responsable ?? '').toLowerCase().includes(firstWord(me)))) return false;
      if (!q) return true;
      return [t.ref, t.titre, t.statut, t.priorite, t.responsable, t.auteur, String(t.id)]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q));
    });
  }, [data, filter, query]);

  const open = data?.tickets.filter((t) => !t.ferme).length ?? 0;

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      keyboardShouldPersistTaps="handled"
      refreshControl={
        <RefreshControl
          refreshing={refreshing}
          onRefresh={() => {
            setRefreshing(true);
            void load(true);
          }}
          tintColor={tokens.colors.accent}
        />
      }
    >
      <ScreenHeader
        title="CSP"
        subtitle={data ? `${open} ouvert${open > 1 ? 's' : ''} · ${data.tickets.length} au total` : 'tickets Tuleap'}
        onBack={onBack}
      />

      {data ? (
        <Panel corners style={styles.scope}>
          <Readout k="tracker" v={`#${data.scope.tracker_id}${data.scope.user ? ` · ${data.scope.user}` : ''}`} />
          <Readout k="sélection" v={data.scope.expert_query || data.scope.query || (data.scope.assigned_to_me ? 'assignés à moi' : 'tous les tickets')} />
          <Readout k="relevé" v={`${formatWhen(data.generated_at)}${data.cached ? ' · cache' : ''}`} />
        </Panel>
      ) : null}

      {error ? <ErrorBlock error={error} onRetry={() => void load(true)} onAccess={onAccess} /> : null}

      {data ? (
        <>
          <Field icon="search" placeholder="Référence, titre, responsable…" value={query} onChangeText={setQuery} returnKeyType="search" />
          <View style={styles.filters}>
            <Chip label={`ouverts · ${open}`} active={filter === 'ouverts'} onPress={() => setFilter('ouverts')} />
            <Chip label={`tous · ${data.tickets.length}`} active={filter === 'tous'} onPress={() => setFilter('tous')} />
            {data.scope.user ? <Chip label="assignés à moi" active={filter === 'moi'} onPress={() => setFilter('moi')} /> : null}
          </View>
        </>
      ) : null}

      {loading ? (
        <>
          <Skeleton lines={3} style={styles.skeleton} />
          <Skeleton lines={3} style={styles.skeleton} />
          <Skeleton lines={3} style={styles.skeleton} />
        </>
      ) : data && tickets.length === 0 ? (
        <Panel>
          <Empty
            icon="inbox"
            title={data.tickets.length === 0 ? 'Aucun ticket dans ce périmètre' : 'Rien ne correspond'}
            message={data.tickets.length === 0 ? 'Le tracker configuré ne rend aucun artefact pour cette sélection.' : 'Change de filtre ou de mots.'}
          />
        </Panel>
      ) : (
        tickets.map((t) => (
          <Row
            key={t.id}
            ticket={t}
            onPress={() => {
              void Haptics.selectionAsync();
              onOpen(t);
            }}
          />
        ))
      )}
    </ScrollView>
  );
}

function ErrorBlock({ error, onRetry, onAccess }: { error: { message: string; status?: number }; onRetry: () => void; onAccess: () => void }) {
  if (error.status === 412) {
    return (
      <Notice tone="warn" icon="key" title="Clé Tuleap manquante">
        <T v="small" tone="muted">
          Les tickets se lisent avec ta clé d’accès personnelle. Saisis-la dans Accès.
        </T>
        <View style={styles.errorActions}>
          <Button label="Ouvrir Accès" variant="secondary" icon="sliders" compact onPress={onAccess} />
        </View>
      </Notice>
    );
  }
  if (error.status === 501) {
    return (
      <Notice tone="warn" icon="server" title="Tuleap non configuré sur le serveur">
        <T v="small" tone="muted">
          Renseigne TULEAP_BASE_URL et TULEAP_CSP_TRACKER_ID côté serveur.
        </T>
      </Notice>
    );
  }
  return (
    <Notice tone="danger" icon="alert-triangle" title={error.status === 401 ? 'Clé Tuleap refusée' : 'Tickets indisponibles'}>
      <T v="small" tone="muted">
        {error.message}
      </T>
      <View style={styles.errorActions}>
        <Button label="Réessayer" variant="secondary" icon="refresh-cw" compact onPress={onRetry} />
        {error.status === 401 ? <Button label="Accès" variant="ghost" icon="sliders" compact onPress={onAccess} /> : null}
      </View>
    </Notice>
  );
}

function Row({ ticket, onPress }: { ticket: CSPTicket; onPress: () => void }) {
  return (
    <Pressable onPress={onPress} accessibilityRole="button" accessibilityLabel={`${ticket.ref} ${ticket.titre}, ${ticket.statut}`} style={({ pressed }) => [styles.row, pressed && styles.pressed]}>
      <View style={styles.rowHead}>
        <T v="mono" tone="accent">
          {ticket.ref}
        </T>
        <View style={styles.flex} />
        {ticket.priorite ? <Badge label={ticket.priorite} tone={priorityTone(ticket.priorite)} /> : null}
        <Badge label={ticket.statut || '—'} tone={ticket.ferme ? 'faint' : 'ok'} />
      </View>
      <T v="bodyStrong" numberOfLines={2}>
        {ticket.titre || '(sans titre)'}
      </T>
      <View style={styles.rowFoot}>
        <Feather name="user" size={11} color={tokens.colors.text3} />
        <T v="mono" tone="faint" numberOfLines={1} style={styles.flex}>
          {ticket.responsable || 'non assigné'}
        </T>
        <T v="mono" tone="faint">
          {formatWhen(ticket.modifie)}
        </T>
      </View>
    </Pressable>
  );
}

/* -------------------------------------------------------------------------- */
/* Détail                                                                      */
/* -------------------------------------------------------------------------- */

export function CSPTicketScreen({ ticket: initial, onBack }: { ticket: CSPTicket; onBack: () => void }) {
  const insets = useSafeAreaInsets();
  const [ticket, setTicket] = useState<CSPTicket>(initial);
  const [comments, setComments] = useState<CSPComment[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .cspTicket(initial.id)
      .then((res) => {
        if (!alive) return;
        // La fiche complète prime sur la ligne de liste, sauf si elle est plus
        // pauvre (droits différents sur certains champs).
        setTicket({ ...initial, ...res.ticket, champs: { ...(initial.champs ?? {}), ...(res.ticket.champs ?? {}) } });
        setComments(res.comments);
      })
      .catch((err: Error) => alive && setError(err.message));
    return () => {
      alive = false;
    };
  }, [initial]);

  const fields = Object.entries(ticket.champs ?? {}).filter(([, v]) => v);

  return (
    <ScrollView style={styles.flex} contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}>
      <ScreenHeader title={ticket.ref} subtitle={ticket.tracker ? `tracker ${ticket.tracker}` : 'ticket Tuleap'} onBack={onBack} />

      <Panel corners>
        <View style={styles.rowHead}>
          {ticket.priorite ? <Badge label={ticket.priorite} tone={priorityTone(ticket.priorite)} /> : null}
          <Badge label={ticket.statut || '—'} tone={ticket.ferme ? 'faint' : 'ok'} />
        </View>
        <T v="title">{ticket.titre || '(sans titre)'}</T>
        <Divider />
        <Readout k="responsable" v={ticket.responsable || 'non assigné'} tone="default" />
        <Readout k="auteur" v={ticket.auteur || '—'} />
        <Readout k="créé" v={formatDate(ticket.cree)} />
        <Readout k="modifié" v={formatDate(ticket.modifie)} />
        {fields.map(([k, v]) => (
          <Readout key={k} k={k} v={v} />
        ))}
        <Button label="Ouvrir dans Tuleap" icon="external-link" variant="secondary" onPress={() => void Linking.openURL(ticket.url)} />
      </Panel>

      {ticket.description ? (
        <Panel>
          <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
            description
          </T>
          <T v="body">{ticket.description}</T>
        </Panel>
      ) : null}

      {error ? (
        <Notice tone="warn" icon="alert-circle">
          <T v="small" tone="muted">
            Détail partiel : {error}
          </T>
        </Notice>
      ) : null}

      <Panel>
        <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
          historique
        </T>
        {comments === null && !error ? (
          <Skeleton lines={2} />
        ) : !comments?.length ? (
          <T v="small" tone="faint">
            Aucun commentaire.
          </T>
        ) : (
          comments.map((c, i) => (
            <View key={`${c.quand}-${i}`} style={styles.comment}>
              <View style={styles.commentHead}>
                <T v="smallStrong">{c.auteur || 'quelqu’un'}</T>
                <T v="mono" tone="faint">
                  {formatDate(c.quand)}
                </T>
              </View>
              <T v="small" tone="muted">
                {c.texte}
              </T>
            </View>
          ))
        )}
      </Panel>
    </ScrollView>
  );
}

/* -------------------------------------------------------------------------- */

function priorityTone(p: string): Tone {
  const s = p.toLowerCase();
  if (/(high|haute|urgent|critical|critique|bloqu|p1|1)/.test(s)) return 'danger';
  if (/(medium|moyenne|normal|p2|2)/.test(s)) return 'warn';
  return 'muted';
}

function firstWord(s: string): string {
  return s.split(/[\s(]/)[0] ?? s;
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const now = new Date();
  const diffMin = Math.round((now.getTime() - d.getTime()) / 60000);
  if (diffMin < 1) return 'à l’instant';
  if (diffMin < 60) return `il y a ${diffMin} min`;
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
  return d.toLocaleDateString('fr-FR', { day: 'numeric', month: 'short' });
}

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.toLocaleDateString('fr-FR')} ${d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' })}`;
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  pressed: { opacity: 0.7 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.md },
  scope: { gap: tokens.space.sm },
  filters: { flexDirection: 'row', flexWrap: 'wrap', gap: tokens.space.sm },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  errorActions: { flexDirection: 'row', gap: tokens.space.sm, marginTop: tokens.space.sm },
  row: {
    gap: tokens.space.sm,
    padding: tokens.space.lg,
    borderWidth: 1,
    borderColor: tokens.colors.line,
    borderRadius: tokens.radius.lg,
    backgroundColor: tokens.colors.panel,
  },
  rowHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  rowFoot: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  comment: { gap: 4, paddingTop: tokens.space.sm, borderTopWidth: 1, borderTopColor: tokens.colors.line },
  commentHead: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: tokens.space.sm },
});
