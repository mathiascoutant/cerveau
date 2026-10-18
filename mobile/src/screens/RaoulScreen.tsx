import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Animated,
  Easing,
  KeyboardAvoidingView,
  Linking,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  TextInput,
  View,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useKeepAwake } from 'expo-keep-awake';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Corners, IconButton, Label, Notice, T } from '../design/ui';
import { Reactor } from '../design/Reactor';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { ChatItem, RaoulState, ToolStep, useRaoul } from '../hooks/useRaoul';
import { api, AssistantMetrics, Membre, SourceStatus } from '../api';
import { loadEquipe, membre, parlerA, useEquipe } from '../lib/equipe';

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
  tuleap: { label: 'CSP', icon: 'tag' },
};

const EXAMPLES = [
  'C’est quoi le dernier mail que j’ai envoyé ?',
  'Qu’est-ce que Xavier a demandé aujourd’hui sur Slack ?',
  'Où en sont mes tickets CSP ?',
  'Je peux aller faire du sport à 10h demain ?',
];

/** Quand le serveur suit ses projets perso : de quoi découvrir l'équipe. */
const PERSO_EXAMPLES = ['Mes projets perso tournent bien ?', 'Passe-moi l’ingénieur logiciel'];

/** Quand un collègue a la parole : son métier, pas celui de Raoul. */
const TEAM_EXAMPLES = [
  'Où en sont mes projets ?',
  'Qu’est-ce qui presse côté sécurité du VPS ?',
  'Le dernier déploiement de VisionShow s’est bien passé ?',
  'Merci, repasse-moi Raoul',
];

type Props = {
  /** Incrémenté à chaque appui sur le widget de l'écran d'accueil. */
  listenRequest?: number;
  /**
   * Une demande à poser dès l'arrivée sur l'écran — « demander un diagnostic »
   * depuis l'onglet Perso. L'identifiant change à chaque nouvelle demande.
   */
  ask?: { id: number; text: string } | null;
};

/**
 * L'écran de conversation. Tout ce que Raoul sait faire passe par ici.
 *
 * En haut, le réacteur dit son état et sert de bouton micro. Au milieu, le
 * fil : les demandes à droite en cyan, les réponses à gauche en ambre, et
 * entre les deux les outils qu'il a consultés, sur une ligne chacun — on voit
 * Raoul travailler au lieu d'attendre devant un cercle. En bas, le composeur,
 * avec un bouton stop tant qu'une réponse est en cours.
 */
export function RaoulScreen({ listenRequest = 0, ask = null }: Props) {
  const raoul = useRaoul();
  const { state, partial, error, thread, start, startConversation, stop, cancel, pushToTalk, askText, voiceAvailable, inConversation } = raoul;
  const [sources, setSources] = useState<SourceStatus[]>([]);
  const [draft, setDraft] = useState('');
  const insets = useSafeAreaInsets();
  const scroll = useRef<ScrollView>(null);
  const equipe = useEquipe();
  // Qui a la parole : c'est lui que l'en-tête montre, et à lui qu'on écrit.
  const speaker = membre(equipe, equipe?.interlocuteur);
  const delegated = speaker.id !== 'raoul';

  useKeepAwake();

  useEffect(() => {
    void loadEquipe();
  }, []);

  // Une demande arrivée d'un autre onglet part tout de suite.
  useEffect(() => {
    if (!ask?.text) return;
    void askText(ask.text);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ask?.id]);

  useEffect(() => {
    let alive = true;
    api
      .status()
      .then((res) => alive && setSources(res.sources))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, [thread.length === 0]);

  // Arrivée par le widget : on allume l'écoute et on saute le mot
  // d'activation — l'appui sur le widget en tient lieu.
  useEffect(() => {
    if (listenRequest === 0 || !voiceAvailable) return;
    void startConversation();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listenRequest]);

  // Le fil suit ce qui arrive : dès qu'un fragment tombe, on descend.
  const last = thread[thread.length - 1];
  useEffect(() => {
    const id = requestAnimationFrame(() => scroll.current?.scrollToEnd({ animated: true }));
    return () => cancelAnimationFrame(id);
  }, [thread.length, last?.text.length, last?.steps?.length]);

  const busy = state === 'thinking';
  const active = state !== 'off';

  const send = useCallback(() => {
    const text = draft.trim();
    if (!text || busy) return;
    setDraft('');
    void askText(text);
  }, [askText, busy, draft]);

  return (
    <KeyboardAvoidingView style={styles.flex} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
      {/* Bandeau : qui a la parole, son état, les sources branchées. */}
      <View style={styles.top}>
        <View style={styles.brand}>
          <View style={[styles.mark, delegated && { borderColor: alpha(speaker.couleur, 0.5), backgroundColor: alpha(speaker.couleur, 0.1) }]}>
            <Corners color={alpha(delegated ? speaker.couleur : tokens.colors.accent, 0.8)} size={6} />
            <T v="label" tone="accent" style={[styles.markText, delegated && { color: speaker.couleur }]}>
              {speaker.nom.slice(0, 1).toUpperCase()}
            </T>
          </View>
          <View style={styles.flexShrink}>
            <T v="h" numberOfLines={1}>
              {speaker.nom}
            </T>
            {delegated && !busy && state !== 'listening' && state !== 'speaking' ? (
              // Au repos, c'est le rôle qui compte : on sait à qui on parle.
              <T v="label" numberOfLines={1} style={{ color: speaker.couleur, textTransform: 'uppercase' }}>
                {speaker.role}
              </T>
            ) : (
              <T v="label" tone={stateTone(state)} style={{ textTransform: 'uppercase' }}>
                {voiceAvailable ? stateLabel(state, speaker) : busy ? stateLabel('thinking', speaker) : 'Mode texte'}
              </T>
            )}
          </View>
        </View>
        {delegated ? (
          <Pressable
            onPress={() => void parlerA('raoul').catch(() => undefined)}
            accessibilityRole="button"
            accessibilityLabel="Rendre la parole à Raoul"
            style={({ pressed }) => [styles.handBack, pressed && styles.pressed]}
          >
            <Feather name="corner-up-left" size={12} color={tokens.colors.raoul} />
            <T v="label" tone="raoul" style={{ textTransform: 'uppercase' }}>
              Raoul
            </T>
          </Pressable>
        ) : (
          <View style={styles.chips}>
            {sources
              .filter((s) => s.connected)
              .map((s) => (
                <SourceDot key={s.provider} icon={SOURCES[s.provider]?.icon ?? 'circle'} label={SOURCES[s.provider]?.label ?? s.provider} warn={Boolean(s.error)} />
              ))}
          </View>
        )}
      </View>

      <ScrollView ref={scroll} style={styles.flex} contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
        <View style={styles.reactorRow}>
          <Reactor state={state} enabled={voiceAvailable} onPress={() => (active ? stop() : start())} onLongPress={pushToTalk} size={thread.length ? 96 : 140} />
          {state === 'listening' && partial ? (
            <T v="body" tone="accent" style={styles.centered}>
              « {partial} »
            </T>
          ) : inConversation && state === 'listening' ? (
            <T v="mono" tone="faint" style={styles.centered}>
              conversation ouverte · « merci Raoul » pour refermer
            </T>
          ) : voiceAvailable && !active && thread.length === 0 ? (
            <T v="mono" tone="faint" style={styles.centered}>
              touche : écoute permanente · appui long : parle tout de suite
            </T>
          ) : null}
        </View>

        {!voiceAvailable && thread.length === 0 ? (
          <Notice tone="info" icon="smartphone" title="Aperçu Expo Go">
            <T v="small" tone="muted">
              « OK Raoul » et l’agenda sont des modules natifs, absents d’Expo Go. Écris ta demande : Raoul répond et lit sa réponse.
            </T>
          </Notice>
        ) : null}

        {error && !thread.some((item) => item.error) ? (
          <Notice tone="danger" icon="alert-triangle">
            <T v="small" tone="danger">
              {error}
            </T>
          </Notice>
        ) : null}

        {thread.length === 0 ? (
          <View style={styles.examples}>
            <Label>Essaie</Label>
            {(delegated ? TEAM_EXAMPLES : equipe ? [...EXAMPLES.slice(0, 2), ...PERSO_EXAMPLES] : EXAMPLES).map((e) => (
              <Pressable key={e} onPress={() => void askText(e)} accessibilityRole="button" accessibilityLabel={`Demander : ${e}`} style={({ pressed }) => [styles.example, pressed && styles.pressed]}>
                <T v="small" style={styles.flex}>
                  {e}
                </T>
                <Feather name="arrow-up-right" size={14} color={tokens.colors.text3} />
              </Pressable>
            ))}
          </View>
        ) : (
          <View style={styles.thread}>
            {thread.map((item, i) => (
              <Bubble key={item.id} item={item} who={item.role === 'raoul' ? membre(equipe, item.agent) : null} showName={item.role === 'raoul' && namesAgent(thread, i)} />
            ))}
          </View>
        )}
      </ScrollView>

      <View style={[styles.composerWrap, { paddingBottom: navBarSpace(insets.bottom) - insets.bottom - tokens.space.md + tokens.space.sm }]}>
        <View style={styles.composer}>
          <TextInput
            value={draft}
            onChangeText={setDraft}
            onSubmitEditing={send}
            placeholder={`Demande quelque chose à ${speaker.nom}`}
            placeholderTextColor={tokens.colors.text3}
            returnKeyType="send"
            blurOnSubmit={false}
            accessibilityLabel={`Écrire une demande à ${speaker.nom}`}
            style={styles.input}
            multiline
          />
          {busy ? (
            <IconButton icon="square" label="Interrompre la réponse" tone="danger" onPress={cancel} />
          ) : (
            <IconButton icon="arrow-up" label="Envoyer" tone="accent" filled disabled={!draft.trim()} onPress={send} />
          )}
        </View>
        {thread.length > 0 ? (
          <Pressable onPress={raoul.clearThread} accessibilityRole="button" accessibilityLabel="Effacer le fil affiché" style={({ pressed }) => [styles.clear, pressed && styles.pressed]}>
            <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
              effacer le fil
            </T>
          </Pressable>
        ) : null}
      </View>
    </KeyboardAvoidingView>
  );
}

/** L'état, dit par celui qui a la parole : c'est Léo qui enquête, pas Raoul. */
function stateLabel(state: RaoulState, speaker: Membre): string {
  if (speaker.id === 'raoul') return STATE_LABEL[state];
  switch (state) {
    case 'thinking':
      return `${speaker.nom} travaille`;
    case 'speaking':
      return `${speaker.nom} répond`;
    case 'waiting':
      return 'Dis « OK Raoul »';
    default:
      return STATE_LABEL[state];
  }
}

/**
 * Le nom se met au-dessus d'une réponse quand celui qui parle change, et sur
 * toute réponse d'un membre de l'équipe : on doit savoir qui parle sans avoir
 * à remonter le fil.
 */
function namesAgent(thread: ChatItem[], index: number): boolean {
  const agent = thread[index].agent ?? 'raoul';
  if (agent !== 'raoul') return true;
  for (let i = index - 1; i >= 0; i--) {
    if (thread[i].role === 'raoul') return (thread[i].agent ?? 'raoul') !== 'raoul';
  }
  return false;
}

function stateTone(state: RaoulState) {
  switch (state) {
    case 'listening':
    case 'waiting':
      return 'accent' as const;
    case 'thinking':
      return 'thinking' as const;
    case 'speaking':
      return 'raoul' as const;
    default:
      return 'faint' as const;
  }
}

function SourceDot({ icon, label, warn }: { icon: React.ComponentProps<typeof Feather>['name']; label: string; warn: boolean }) {
  const color = warn ? tokens.colors.warn : tokens.colors.text2;
  return (
    <View style={styles.sourceDot} accessibilityLabel={`${label}${warn ? ', en erreur' : ', connecté'}`}>
      <Feather name={icon} size={12} color={color} />
      <T v="label" style={{ color, textTransform: 'uppercase' }}>
        {label}
      </T>
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Le fil                                                                      */
/* -------------------------------------------------------------------------- */

function Bubble({ item, who, showName }: { item: ChatItem; who: Membre | null; showName: boolean }) {
  const fade = useRef(new Animated.Value(0)).current;
  const color = who?.couleur ?? tokens.colors.raoul;
  useEffect(() => {
    Animated.timing(fade, { toValue: 1, duration: tokens.motion.base, easing: Easing.bezier(...tokens.motion.easing), useNativeDriver: true }).start();
  }, [fade]);

  if (item.role === 'user') {
    return (
      <Animated.View style={[styles.userRow, { opacity: fade, transform: [{ translateY: fade.interpolate({ inputRange: [0, 1], outputRange: [6, 0] }) }] }]}>
        <View style={styles.userBubble}>
          <T v="body" style={{ color: tokens.colors.text }}>
            {item.text}
          </T>
        </View>
      </Animated.View>
    );
  }

  const { links, text } = splitLinks(item.text);
  return (
    <Animated.View style={[styles.raoulRow, { opacity: fade, transform: [{ translateY: fade.interpolate({ inputRange: [0, 1], outputRange: [6, 0] }) }] }]}>
      {showName && who ? (
        <View style={styles.speakerTag} accessibilityLabel={`${who.nom}, ${who.role}`}>
          <View style={[styles.speakerDot, { backgroundColor: color }]} />
          <T v="label" style={{ color, textTransform: 'uppercase' }}>
            {who.id === 'raoul' ? 'Raoul' : `${who.nom} · ${who.role}`}
          </T>
        </View>
      ) : null}
      {item.notes?.map((note, i) => (
        <T key={`note-${i}`} v="small" tone="muted" style={styles.note}>
          {note}
        </T>
      ))}
      {item.steps?.length ? (
        <View style={styles.steps}>
          {item.steps.map((step, i) => (
            <StepLine key={`${step.tool}-${i}`} step={step} />
          ))}
        </View>
      ) : null}

      {item.text || !item.streaming ? (
        <View style={[styles.raoulBubble, item.error && styles.raoulBubbleError]}>
          <View style={[styles.raoulRule, { backgroundColor: item.error ? tokens.colors.danger : color }]} />
          <View style={styles.flex}>
            {/* Remonté à la fin du flux : sans ça, iOS garde parfois la mise
                en page d'un texte encore en train d'arriver, et la dernière
                ligne file hors de la bulle au lieu de passer à la suivante. */}
            <T v="body" key={item.streaming ? 'live' : 'done'}>
              {text}
              {item.streaming ? <T v="body" style={{ color }}>▍</T> : null}
            </T>
            {links.map((url) => (
              <Pressable key={url} onPress={() => void Linking.openURL(url)} accessibilityRole="link" accessibilityLabel={url} style={({ pressed }) => [styles.link, pressed && styles.pressed]}>
                <Feather name="external-link" size={12} color={tokens.colors.accent} />
                <T v="mono" tone="accent" numberOfLines={1} style={styles.flex}>
                  {url.replace(/^https?:\/\//, '')}
                </T>
              </Pressable>
            ))}
          </View>
        </View>
      ) : item.streaming && (!item.steps?.length || item.steps.every((st) => st.ms !== undefined)) ? (
        // Entre deux séries d'outils, le modèle réfléchit sans rien émettre :
        // sans cette ligne, toutes les étapes sont cochées et plus rien ne
        // bouge, comme si la réponse s'était perdue.
        <View style={styles.waiting}>
          <ActivityIndicator size="small" color={tokens.colors.thinking} />
          <T v="mono" tone="thinking">
            {item.steps?.length ? `${who?.id === 'raoul' || !who ? 'Raoul' : who.nom} recoupe ce qu’il a trouvé…` : '…'}
          </T>
        </View>
      ) : null}

      {item.effects?.map((effect) => (
        <View key={effect} style={styles.effect}>
          <Feather name="check-circle" size={12} color={tokens.colors.ok} />
          <T v="mono" tone="ok" style={styles.flex}>
            {effect}
          </T>
        </View>
      ))}

      {item.metrics ? <MetricsLine m={item.metrics} /> : null}
    </Animated.View>
  );
}

/** « ⚙ Recherche Slack · 1,2 s » — la ligne d'outil, centrée, comme un relevé. */
function StepLine({ step }: { step: ToolStep }) {
  const done = step.ms !== undefined;
  const color = step.err ? tokens.colors.danger : done ? tokens.colors.text3 : tokens.colors.thinking;
  return (
    <View style={styles.step} accessibilityLabel={`${step.label}${done ? `, ${fmtMs(step.ms ?? 0)}` : ', en cours'}`}>
      {done ? <Feather name={step.err ? 'x-circle' : 'check'} size={11} color={color} /> : <ActivityIndicator size="small" color={color} style={styles.stepSpinner} />}
      <T v="mono" style={{ color }}>
        {step.label}
        {done ? ` · ${fmtMs(step.ms ?? 0)}` : ''}
      </T>
    </View>
  );
}

/**
 * Les mesures sous chaque réponse : l'étage, le modèle, le premier mot, le
 * total, les tokens. Discret, mais toujours là — c'est ce qui permet de
 * dire pourquoi une réponse a traîné au lieu de le supposer.
 */
function MetricsLine({ m }: { m: AssistantMetrics }) {
  const [open, setOpen] = useState(false);
  const tier = m.tier === 'fast' ? 'rapide' : m.tier === 'deep' ? 'fort' : 'outils';
  return (
    <Pressable onPress={() => setOpen((v) => !v)} accessibilityRole="button" accessibilityLabel="Mesures de la réponse" style={styles.metrics}>
      <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
        {tier} · {shortModel(m.model)} · 1er mot {fmtMs(m.first_token_ms)} · total {fmtMs(m.total_ms)}
      </T>
      {open ? (
        <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
          {m.model_calls} appel{m.model_calls > 1 ? 's' : ''} · {m.input_tokens} tok. entrée
          {m.cached_tokens ? ` (${m.cached_tokens} en cache)` : ''} · {m.output_tokens} tok. sortie
          {m.reason ? ` · ${m.reason}` : ''}
        </T>
      ) : null}
    </Pressable>
  );
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(1).replace('.', ',')} s`;
}

function shortModel(model: string): string {
  return model.replace(/^gpt-/, '').replace(/-\d{4}-\d{2}-\d{2}$/, '');
}

/**
 * Les espaces insécables que le modèle sème parfois entre les mots d'un
 * paragraphe entier empêchent toute coupure : la ligne file hors de l'écran
 * au lieu de passer à la suivante. On n'en garde que devant la ponctuation
 * haute, où la typographie française les veut.
 */
function breakableSpaces(text: string): string {
  return text.replace(/[\u00A0\u202F\u2007](?![?!:;»%])/g, ' ');
}

/** Sort les URL posées seules sur leur ligne en fin de réponse : elles se
 *  rendent en lien, pas en texte. */
function splitLinks(raw: string): { text: string; links: string[] } {
  const text = breakableSpaces(raw);
  const lines = text.split('\n');
  const links: string[] = [];
  while (lines.length > 0) {
    const last = lines[lines.length - 1].trim();
    if (/^https?:\/\/\S+$/.test(last)) {
      links.unshift(last);
      lines.pop();
    } else if (last === '' && links.length > 0) {
      lines.pop();
    } else {
      break;
    }
  }
  return { text: lines.join('\n').trimEnd(), links };
}

/* -------------------------------------------------------------------------- */

const styles = StyleSheet.create({
  flex: { flex: 1 },
  flexShrink: { flexShrink: 1 },
  note: { fontStyle: 'italic', marginBottom: tokens.space.xs },
  centered: { textAlign: 'center' },
  pressed: { opacity: 0.6 },
  handBack: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    minHeight: 32,
    paddingHorizontal: tokens.space.md,
    borderRadius: tokens.radius.pill,
    borderWidth: 1,
    borderColor: alpha(tokens.colors.raoul, 0.45),
    backgroundColor: alpha(tokens.colors.raoul, 0.08),
  },
  speakerTag: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  speakerDot: { width: 6, height: 6, borderRadius: 3 },

  top: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: tokens.space.lg,
    paddingVertical: tokens.space.sm,
    gap: tokens.space.md,
  },
  brand: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, flexShrink: 1 },
  mark: {
    width: 34,
    height: 34,
    alignItems: 'center',
    justifyContent: 'center',
    borderWidth: 1,
    borderColor: alpha(tokens.colors.accent, 0.35),
    backgroundColor: alpha(tokens.colors.accent, 0.08),
  },
  markText: { fontSize: 15, letterSpacing: 0 },
  chips: { flexDirection: 'row', flexWrap: 'wrap', justifyContent: 'flex-end', gap: tokens.space.sm, flexShrink: 1 },
  sourceDot: { flexDirection: 'row', alignItems: 'center', gap: 4 },

  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, paddingBottom: tokens.space.lg, gap: tokens.space.lg },
  reactorRow: { alignItems: 'center', gap: tokens.space.xs },

  examples: { gap: tokens.space.sm },
  example: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: tokens.space.md,
    minHeight: tokens.touch,
    paddingHorizontal: tokens.space.lg,
    paddingVertical: tokens.space.md,
    borderWidth: 1,
    borderColor: tokens.colors.line,
    borderRadius: tokens.radius.md,
    backgroundColor: tokens.colors.panel,
  },

  thread: { gap: tokens.space.lg },
  userRow: { alignItems: 'flex-end' },
  userBubble: {
    maxWidth: '86%',
    paddingHorizontal: tokens.space.lg,
    paddingVertical: tokens.space.md,
    borderRadius: tokens.radius.lg,
    borderTopRightRadius: tokens.radius.sm,
    backgroundColor: alpha(tokens.colors.accent, 0.12),
    borderWidth: 1,
    borderColor: alpha(tokens.colors.accent, 0.35),
  },
  raoulRow: { gap: tokens.space.sm },
  steps: { alignItems: 'center', gap: 4 },
  step: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  stepSpinner: { transform: [{ scale: 0.6 }] },
  // Pas de `gap` ici : sur iOS, un texte dans une rangée à `gap` est mesuré
  // sur une largeur et dessiné sur une autre — le dernier mot sortait de la
  // bulle, coupé, avec une ligne vide en dessous. Une marge ne le fait pas.
  raoulBubble: {
    flexDirection: 'row',
    paddingRight: tokens.space.sm,
  },
  raoulBubbleError: { opacity: 0.85 },
  raoulRule: { width: 2, borderRadius: 1, marginTop: 4, marginRight: tokens.space.md },
  link: { flexDirection: 'row', alignItems: 'center', gap: 6, marginTop: tokens.space.sm },
  waiting: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  effect: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm, paddingLeft: tokens.space.lg },
  metrics: { paddingLeft: tokens.space.lg, gap: 2 },

  composerWrap: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xs },
  composer: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    gap: tokens.space.sm,
    paddingLeft: tokens.space.lg,
    paddingRight: tokens.space.xs,
    paddingVertical: tokens.space.xs,
    borderWidth: 1,
    borderColor: tokens.colors.lineStrong,
    borderRadius: tokens.radius.xl,
    backgroundColor: tokens.colors.panel,
  },
  input: {
    flex: 1,
    minHeight: tokens.touch - 6,
    maxHeight: 120,
    paddingVertical: tokens.space.sm,
    color: tokens.colors.text,
    ...tokens.type.body,
  },
  clear: { alignSelf: 'center', paddingVertical: tokens.space.xs },
});
