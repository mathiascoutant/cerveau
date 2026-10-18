import React, { useCallback, useEffect, useState } from 'react';
import { Linking, Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Badge, Button, Divider, Dot, Empty, IconButton, Notice, Panel, Readout, ScreenHeader, Section, Skeleton, T, Tone } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { Alerte, EtatBase, EtatComposant, EtatNiveau, EtatProjet, EtatProjets, EtatVPS } from '../api';
import { useSlot } from '../lib/cache';
import { loadProjets, PROJETS_KEY } from '../lib/equipe';

/**
 * Projets : ce qui tourne sur le VPS, ce qui répond, ce qui est déployé.
 *
 * L'écran se lit du plus grave au plus calme : d'abord les alertes — ce qui
 * mérite qu'on agisse —, puis la machine, puis chaque projet et ses
 * composants. Une ligne verte ne dit rien de plus que « ça va » ; c'est le
 * reste qu'on vient chercher. Un projet se touche pour le détail, et se confie
 * à l'ingénieur d'un geste quand il faut comprendre.
 */
export function ProjetsScreen({ onAsk }: { onAsk: (text: string, agent: string) => void }) {
  const insets = useSafeAreaInsets();
  const slot = useSlot<EtatProjets | false>(PROJETS_KEY);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void loadProjets();
  }, []);

  const refresh = useCallback(() => {
    setBusy(true);
    void loadProjets(true).finally(() => setBusy(false));
  }, []);

  const etat = slot.data || null;

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      refreshControl={<RefreshControl refreshing={busy} onRefresh={refresh} tintColor={tokens.colors.perso} />}
    >
      <ScreenHeader
        title="Projets"
        subtitle={etat ? `relevé ${ago(etat.genere_le)}` : 'ce qui tourne sur ton VPS'}
        right={<IconButton icon="refresh-cw" label="Refaire les relevés" onPress={refresh} disabled={busy || slot.loading} />}
      />

      {slot.data === false ? (
        <Empty icon="folder" title="Aucun projet suivi" message="Le serveur n’a pas de registre de projets (PROJETS_REGISTRE). Une fois posé, tes projets apparaissent ici." />
      ) : null}

      {slot.error ? (
        <Notice tone="warn" icon="wifi-off" title="Relevé impossible">
          <T v="small" tone="muted">
            {slot.error}
          </T>
        </Notice>
      ) : null}

      {!etat && slot.data !== false && !slot.error ? (
        <>
          <T v="mono" tone="faint">
            relevé en cours : VPS, sites, GitHub, bases…
          </T>
          <Skeleton lines={3} style={styles.skeleton} />
          <Skeleton lines={4} style={styles.skeleton} />
        </>
      ) : null}

      {etat ? (
        <>
          {etat.erreurs?.length ? (
            <Notice tone="warn" icon="alert-triangle" title="Relevés incomplets">
              {etat.erreurs.map((e) => (
                <T key={e} v="small" tone="muted">
                  {e}
                </T>
              ))}
            </Notice>
          ) : null}
          <Alertes alertes={etat.alertes} />
          {etat.vps ? <VPS vps={etat.vps} /> : null}
          <Section title="Projets" count={etat.projets.length}>
            {etat.projets.map((p) => (
              <Projet key={p.id} projet={p} onAsk={onAsk} />
            ))}
          </Section>
          <Capacites etat={etat} />
        </>
      ) : null}
    </ScrollView>
  );
}

/* -------------------------------------------------------------------------- */

const LEVEL: Record<Alerte['niveau'], { icon: React.ComponentProps<typeof Feather>['name']; tone: Tone }> = {
  critique: { icon: 'alert-octagon', tone: 'danger' },
  attention: { icon: 'alert-triangle', tone: 'warn' },
  info: { icon: 'info', tone: 'faint' },
};

function Alertes({ alertes }: { alertes: Alerte[] }) {
  const [all, setAll] = useState(false);
  if (alertes.length === 0) {
    return (
      <Notice tone="ok" icon="check-circle">
        <T v="small" tone="muted">
          Rien à signaler : tout répond, tout est à jour.
        </T>
      </Notice>
    );
  }
  const shown = all ? alertes : alertes.slice(0, 5);
  const critical = alertes.filter((a) => a.niveau === 'critique').length;
  return (
    <Section title="À voir" count={alertes.length}>
      <Panel tone={critical ? 'danger' : 'warn'} corners>
        {shown.map((a, i) => (
          <View key={`${a.texte}-${i}`} style={styles.alert}>
            <Feather name={LEVEL[a.niveau].icon} size={14} color={toneColorOf(LEVEL[a.niveau].tone)} style={styles.alertIcon} />
            <T v="small" tone={a.niveau === 'info' ? 'muted' : 'default'} style={styles.flex}>
              {a.texte}
            </T>
          </View>
        ))}
        {alertes.length > 5 ? (
          <Pressable onPress={() => setAll((v) => !v)} accessibilityRole="button" style={styles.more}>
            <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
              {all ? 'réduire' : `et ${alertes.length - 5} de plus`}
            </T>
          </Pressable>
        ) : null}
      </Panel>
    </Section>
  );
}

function VPS({ vps }: { vps: EtatVPS }) {
  const disk = vps.disques[0];
  const memPct = vps.memoire.total_mo ? Math.round(((vps.memoire.total_mo - vps.memoire.disponible_mo) / vps.memoire.total_mo) * 100) : 0;
  const running = vps.conteneurs?.filter((c) => c.etat === 'running').length ?? 0;
  return (
    <Section title="Le VPS">
      <Panel>
        <View style={styles.rowBetween}>
          <View style={styles.flex}>
            <T v="h">{vps.nom}</T>
            <T v="mono" tone="faint">
              {vps.os} · noyau {vps.noyau.split('-').slice(0, 2).join('-')}
            </T>
          </View>
          {vps.hors_support ? <Badge label="hors support" tone="danger" /> : vps.fin_support ? <Badge label={`support → ${shortDate(vps.fin_support)}`} tone="ok" /> : null}
        </View>
        {disk ? <Gauge label="Disque" pct={disk.utilise_pct} detail={`${disk.libre_go.toFixed(1).replace('.', ',')} Go libres sur ${Math.round(disk.total_go)}`} /> : null}
        <Gauge label="Mémoire" pct={memPct} detail={`${(vps.memoire.disponible_mo / 1024).toFixed(1).replace('.', ',')} Go disponibles`} />
        <Divider />
        <Readout k="allumé depuis" v={uptime(vps.allume_depuis_s)} />
        <Readout k="charge" v={vps.charge.map((c) => c.toFixed(2).replace('.', ',')).join(' · ')} />
        <Readout
          k="mises à jour"
          v={vps.mises_a_jour < 0 ? 'inconnues' : `${vps.mises_a_jour}${vps.mises_a_jour_securite ? ` dont ${vps.mises_a_jour_securite} sécurité` : ''}`}
          tone={vps.mises_a_jour_securite ? 'warn' : 'muted'}
        />
        {vps.redemarrage_requis ? <Readout k="redémarrage" v="requis" tone="warn" /> : null}
        <Readout k="conteneurs" v={`${running} en marche sur ${vps.conteneurs?.length ?? 0}`} />
        {vps.docker ? <Readout k="docker" v={vps.docker} /> : null}
      </Panel>
    </Section>
  );
}

function Gauge({ label, pct, detail }: { label: string; pct: number; detail: string }) {
  const color = pct >= 90 ? tokens.colors.danger : pct >= 80 ? tokens.colors.warn : tokens.colors.perso;
  return (
    <View style={styles.gauge} accessibilityLabel={`${label} : ${pct} %, ${detail}`}>
      <View style={styles.rowBetween}>
        <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
          {label}
        </T>
        <T v="mono" tone="muted">
          {pct} % · {detail}
        </T>
      </View>
      <View style={styles.track}>
        <View style={[styles.fill, { width: `${Math.min(Math.max(pct, 2), 100)}%`, backgroundColor: color }]} />
      </View>
    </View>
  );
}

const STATE: Record<EtatNiveau, { label: string; tone: Tone }> = {
  ok: { label: 'ok', tone: 'ok' },
  attention: { label: 'à surveiller', tone: 'warn' },
  panne: { label: 'en panne', tone: 'danger' },
  inconnu: { label: 'inconnu', tone: 'faint' },
};

function Projet({ projet, onAsk }: { projet: EtatProjet; onAsk: (text: string, agent: string) => void }) {
  const st = STATE[projet.etat];
  return (
    <Panel tone={projet.etat === 'panne' ? 'danger' : projet.etat === 'attention' ? 'warn' : undefined}>
      <View style={styles.rowBetween}>
        <View style={styles.flex}>
          <T v="h">{projet.nom}</T>
          {projet.description ? (
            <T v="small" tone="faint" numberOfLines={2}>
              {projet.description}
            </T>
          ) : null}
        </View>
        <Badge label={st.label} tone={st.tone} />
      </View>
      <Divider />
      {projet.composants.map((c) => (
        <Composant key={c.id} c={c} />
      ))}
      {projet.bases?.map((b) => (
        <Base key={b.id} b={b} />
      ))}
      <Button
        label="Diagnostic par l’ingénieur"
        variant="secondary"
        icon="cpu"
        compact
        onPress={() =>
          onAsk(
            `Fais-moi un diagnostic de ${projet.nom} : ce qui répond, ce qui est déployé par rapport au dernier commit, la CI, les bases, et ce qui mérite d'être corrigé.`,
            'ingenieur',
          )
        }
      />
    </Panel>
  );
}

function Composant({ c }: { c: EtatComposant }) {
  const [open, setOpen] = useState(false);
  const st = STATE[c.etat];
  const failed = c.depot?.ci?.filter((r) => r.statut === 'completed' && r.conclusion === 'failure') ?? [];
  const latency = c.http ? (c.http.erreur ? 'injoignable' : c.http.statut && c.http.statut >= 400 ? `${c.http.statut}` : fmtMs(c.http.total_ms)) : c.type === 'app' ? 'app' : '';
  const facts: string[] = [];
  if (c.conteneur?.demarre_le) facts.push(`déployé ${ago(c.conteneur.demarre_le)}`);
  if (c.depot?.deploiement === 'a_jour') facts.push('à jour');
  if (c.depot?.deploiement === 'en_retard') facts.push('en retard sur la branche');
  if (c.depot?.dernier_commit && c.type === 'app') facts.push(`dernier commit ${ago(c.depot.dernier_commit.date)}`);
  if (failed.length) facts.push(`CI en échec`);

  return (
    <View>
      <Pressable onPress={() => setOpen((v) => !v)} accessibilityRole="button" accessibilityLabel={`${c.nom}, ${st.label}`} style={({ pressed }) => [styles.line, pressed && styles.pressed]}>
        <Dot tone={st.tone} glow={c.etat === 'panne'} />
        <View style={styles.flex}>
          <View style={styles.lineHead}>
            <T v="bodyStrong" numberOfLines={1} style={styles.flexShrink}>
              {c.nom}
            </T>
            {c.env && c.env !== 'prod' ? <Badge label={c.env} tone="faint" /> : null}
          </View>
          {facts.length ? (
            <T v="mono" tone={failed.length || c.depot?.deploiement === 'en_retard' ? 'warn' : 'faint'} numberOfLines={1}>
              {facts.join(' · ')}
            </T>
          ) : null}
        </View>
        <T v="mono" tone={c.http && !c.http.erreur && (c.http.statut ?? 0) < 400 ? (c.http.total_ms > 2500 ? 'warn' : 'muted') : c.http ? 'danger' : 'faint'}>
          {latency}
        </T>
        <Feather name={open ? 'chevron-up' : 'chevron-down'} size={14} color={tokens.colors.text3} />
      </Pressable>
      {open ? <ComposantDetail c={c} /> : null}
    </View>
  );
}

function ComposantDetail({ c }: { c: EtatComposant }) {
  return (
    <View style={styles.detail}>
      {c.http ? (
        <>
          <Readout k="adresse" v={c.http.url.replace(/^https?:\/\//, '')} />
          {c.http.erreur ? (
            <Readout k="erreur" v={c.http.erreur} tone="danger" />
          ) : (
            <Readout
              k="réponse"
              v={`${c.http.statut} en ${fmtMs(c.http.total_ms)}${c.http.premier_octet_ms ? ` · appli ${fmtMs(c.http.premier_octet_ms)}` : ''}`}
            />
          )}
          {c.http.certificat_jours !== undefined && c.http.certificat_expire ? (
            <Readout k="certificat" v={`${c.http.certificat_jours} jours`} tone={c.http.certificat_jours < 14 ? 'warn' : 'muted'} />
          ) : null}
        </>
      ) : null}
      {c.conteneur ? (
        <Readout
          k="conteneur"
          v={`${c.conteneur.nom} · ${c.conteneur.etat}${c.conteneur.redemarrages ? ` · ${c.conteneur.redemarrages} redém.` : ''}`}
          tone={c.conteneur.etat === 'running' ? 'muted' : 'danger'}
        />
      ) : null}
      {c.depot ? (
        <>
          <Readout k="dépôt" v={`${c.depot.depot} · ${c.depot.branche}`} />
          {c.depot.dernier_commit ? (
            <T v="small" tone="muted">
              {c.depot.dernier_commit.message} — {c.depot.dernier_commit.auteur ?? '?'}, {ago(c.depot.dernier_commit.date)}
            </T>
          ) : null}
          {c.depot.explication ? (
            <T v="small" tone={c.depot.deploiement === 'en_retard' ? 'warn' : 'faint'}>
              {capitalize(c.depot.explication)}.
            </T>
          ) : null}
          {c.depot.erreur ? (
            <T v="small" tone="warn">
              {c.depot.erreur}
            </T>
          ) : null}
          {c.depot.ci?.map((run) => (
            <Pressable key={run.nom} onPress={() => run.lien && void Linking.openURL(run.lien)} accessibilityRole="link" style={styles.run}>
              <Feather name={runIcon(run.statut, run.conclusion)} size={12} color={runColor(run.statut, run.conclusion)} />
              <T v="mono" tone="muted" numberOfLines={1} style={styles.flex}>
                {run.nom} · {ago(run.date)}
              </T>
            </Pressable>
          ))}
        </>
      ) : null}
    </View>
  );
}

function Base({ b }: { b: EtatBase }) {
  const tone: Tone = b.etat === 'ok' ? 'ok' : b.etat === 'erreur' ? 'danger' : 'faint';
  return (
    <View style={styles.line} accessibilityLabel={`Base ${b.nom}, ${b.etat}`}>
      <Feather name="database" size={13} color={toneColorOf(tone)} />
      <View style={styles.flex}>
        <T v="bodyStrong" numberOfLines={1}>
          {b.nom}
        </T>
        <T v="mono" tone={b.etat === 'erreur' ? 'danger' : 'faint'} numberOfLines={2}>
          {b.etat === 'erreur'
            ? (b.erreur ?? 'injoignable')
            : b.etat === 'ok'
              ? [b.nom_base, b.version ? `Mongo ${b.version}` : '', b.donnees_mo !== undefined ? `${fmtMo(b.donnees_mo)}` : ''].filter(Boolean).join(' · ')
              : 'non relevée'}
        </T>
      </View>
      {b.ping_ms !== undefined && b.etat === 'ok' ? (
        <T v="mono" tone={b.ping_ms > 300 ? 'warn' : 'muted'}>
          {fmtMs(b.ping_ms)}
        </T>
      ) : null}
    </View>
  );
}

function Capacites({ etat }: { etat: EtatProjets }) {
  const c = etat.capacites;
  return (
    <View style={styles.caps}>
      <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
        sonde VPS {c.sonde ? '✓' : '✗'} · github {c.github ? (c.github_authentifie ? '✓' : 'public seulement') : '✗'}
      </T>
    </View>
  );
}

/* -------------------------------------------------------------------------- */

function toneColorOf(tone: Tone): string {
  switch (tone) {
    case 'danger':
      return tokens.colors.danger;
    case 'warn':
      return tokens.colors.warn;
    case 'ok':
      return tokens.colors.ok;
    case 'faint':
      return tokens.colors.text3;
    default:
      return tokens.colors.text2;
  }
}

function runIcon(status: string, conclusion?: string): React.ComponentProps<typeof Feather>['name'] {
  if (status !== 'completed') return 'loader';
  if (conclusion === 'success') return 'check-circle';
  if (conclusion === 'failure') return 'x-circle';
  return 'minus-circle';
}

function runColor(status: string, conclusion?: string): string {
  if (status !== 'completed') return tokens.colors.thinking;
  if (conclusion === 'success') return tokens.colors.ok;
  if (conclusion === 'failure') return tokens.colors.danger;
  return tokens.colors.text3;
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(1).replace('.', ',')} s`;
}

function fmtMo(mo: number): string {
  if (mo >= 1024) return `${(mo / 1024).toFixed(1).replace('.', ',')} Go`;
  return `${mo.toFixed(mo < 10 ? 1 : 0).replace('.', ',')} Mo`;
}

function uptime(seconds: number): string {
  const days = Math.floor(seconds / 86400);
  if (days >= 1) return `${days} jour${days > 1 ? 's' : ''}`;
  const hours = Math.floor(seconds / 3600);
  return `${hours} h`;
}

/** « il y a 3 min », « hier », « le 12/10 » — l'écart compte plus que l'heure. */
function ago(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const diff = Date.now() - t;
  const min = Math.round(diff / 60000);
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

function shortDate(day: string): string {
  const [y, m] = day.split('-');
  return `${m}/${y}`;
}

function capitalize(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  flexShrink: { flexShrink: 1 },
  pressed: { opacity: 0.65 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xl },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  rowBetween: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: tokens.space.md },
  alert: { flexDirection: 'row', gap: tokens.space.sm },
  alertIcon: { marginTop: 3 },
  more: { alignSelf: 'flex-start', paddingVertical: tokens.space.xs },
  gauge: { gap: 6 },
  track: { height: 6, borderRadius: 3, backgroundColor: tokens.colors.panelPressed, overflow: 'hidden' },
  fill: { height: 6, borderRadius: 3 },
  line: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, minHeight: tokens.touch, paddingVertical: 4 },
  lineHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  detail: {
    gap: 6,
    marginLeft: tokens.space.lg,
    paddingLeft: tokens.space.md,
    paddingBottom: tokens.space.sm,
    borderLeftWidth: 1,
    borderLeftColor: alpha(tokens.colors.perso, 0.35),
  },
  run: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm, minHeight: 28 },
  caps: { alignItems: 'center', paddingBottom: tokens.space.md },
});
