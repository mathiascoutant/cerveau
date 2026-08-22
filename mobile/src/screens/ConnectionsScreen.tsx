import React, { useCallback, useEffect, useState } from 'react';
import { Alert, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';

import {
  Banner,
  Button,
  Card,
  Divider,
  Field,
  IconName,
  ScreenHeader,
  SectionLabel,
  StatusDot,
  Txt,
} from '../components/ui';
import {
  api,
  Connection,
  detectedApiUrl,
  getApiUrl,
  Provider,
  setApiUrl,
  VoiceInfo,
} from '../api';
import {
  calendarSupported,
  hasCalendarAccess,
  requestCalendarAccess,
  syncCalendar,
} from '../lib/calendar';
import { frenchVoices } from '../lib/speech';
import { theme } from '../theme';

export function ConnectionsScreen() {
  const [connections, setConnections] = useState<Record<string, Connection>>({});
  const [serverUrl, setServerUrl] = useState('');
  const [name, setName] = useState('');
  const [voices, setVoices] = useState<{ label: string; best: boolean }[] | null>(null);
  const [voice, setVoice] = useState<VoiceInfo | null>(null);
  const [calendarReady, setCalendarReady] = useState(false);
  const [calendarUsable, setCalendarUsable] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const res = await api.connections();
      const map: Record<string, Connection> = {};
      for (const c of res.connections) map[c.provider] = c;
      setConnections(map);
    } catch {
      // serveur injoignable : l'écran reste utilisable pour corriger l'URL
    }
    setCalendarUsable(await calendarSupported());
    setCalendarReady(await hasCalendarAccess());
  }, []);

  useEffect(() => {
    void getApiUrl().then(setServerUrl);
    void api
      .me()
      .then((me) => {
        setName(me.name ?? '');
        if (me.voice) setVoice(me.voice);
      })
      .catch(() => undefined);
    void frenchVoices()
      .then((list) =>
        setVoices(list.map((v, i) => ({ label: `${v.name} · ${v.quality}`, best: i === 0 }))),
      )
      .catch(() => setVoices([]));
    void refresh();
  }, [refresh]);

  const run = useCallback(
    async (key: string, task: () => Promise<void>) => {
      setBusy(key);
      try {
        await task();
        await refresh();
      } catch (err) {
        Alert.alert('Échec', (err as Error).message);
      } finally {
        setBusy(null);
      }
    },
    [refresh],
  );

  const disconnect = (provider: Provider) =>
    run(`disconnect-${provider}`, async () => {
      await api.disconnect(provider);
    });

  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={styles.content}
      keyboardShouldPersistTaps="handled"
    >
      <ScreenHeader
        title="Accès"
        subtitle="Raoul ne voit que ce que tu lui ouvres. Tout est chiffré côté serveur."
      />

      <Card>
        <SectionLabel>Identité</SectionLabel>
        <Field
          label="Ton prénom"
          placeholder="Mathias"
          value={name}
          onChangeText={setName}
          autoCapitalize="words"
          hint="Raoul s’en sert pour s’adresser à toi, et calque son ton sur le tien."
        />
        <Button
          label="Enregistrer"
          variant="secondary"
          icon="check"
          loading={busy === 'name'}
          onPress={() => run('name', async () => void (await api.setName(name.trim())))}
        />
      </Card>

      <Card>
        <SectionLabel>Serveur</SectionLabel>
        <Field
          label="Adresse du backend"
          placeholder="https://cerveau.mondomaine.fr"
          value={serverUrl}
          onChangeText={setServerUrl}
          keyboardType="url"
          hint={`Détectée automatiquement : ${detectedApiUrl()}`}
        />
        <Button
          label="Enregistrer l’adresse"
          variant="secondary"
          icon="server"
          loading={busy === 'server'}
          onPress={() =>
            run('server', async () => {
              await setApiUrl(serverUrl);
              await api.status();
            })
          }
        />
      </Card>

      <SectionLabel>Sources</SectionLabel>

      <Source
        icon="calendar"
        title="Agenda iOS"
        connected={calendarReady}
        hint="Tous les calendriers du téléphone — iCloud, Google, Exchange. Raoul y écrit les créneaux qu’il valide."
      >
        {calendarUsable ? (
          <Button
            label={calendarReady ? 'Resynchroniser' : 'Autoriser l’accès'}
            variant={calendarReady ? 'secondary' : 'primary'}
            icon={calendarReady ? 'refresh-cw' : 'unlock'}
            loading={busy === 'calendar'}
            onPress={() =>
              run('calendar', async () => {
                const granted = await requestCalendarAccess();
                if (!granted) throw new Error('Accès refusé dans les réglages iOS.');
                const count = await syncCalendar();
                Alert.alert('Agenda synchronisé', `${count} événements envoyés à Raoul.`);
              })
            }
          />
        ) : (
          <Banner tone="warning" icon="smartphone">
            <Txt variant="small" tone="muted">
              Indisponible dans Expo Go : expo-calendar est un module natif. Disponible dès le dev
              build.
            </Txt>
          </Banner>
        )}
      </Source>

      <GandiSource
        connection={connections.gandi}
        busy={busy === 'gandi'}
        onConnect={(email, password) =>
          run('gandi', async () => void (await api.connectGandi(email, password)))
        }
        onDisconnect={() => disconnect('gandi')}
      />

      <SlackSource
        connection={connections.slack}
        busy={busy === 'slack'}
        onAuthorize={() =>
          run('slack', async () => {
            const { url } = await api.startSlackOAuth();
            const { Linking } = require('react-native') as typeof import('react-native');
            await Linking.openURL(url);
          })
        }
        onConnect={(token) => run('slack', async () => void (await api.connectSlack(token)))}
        onDisconnect={() => disconnect('slack')}
      />

      <WhatsAppSource
        connection={connections.whatsapp}
        busy={busy === 'whatsapp'}
        onConnect={(phoneId, token) =>
          run('whatsapp', async () => void (await api.connectWhatsApp(phoneId, token)))
        }
        onDisconnect={() => disconnect('whatsapp')}
      />

      <Card>
        <SectionLabel>Voix de Raoul</SectionLabel>

        <View style={styles.voiceRow}>
          <Feather
            name={voice?.engine === 'elevenlabs' ? 'check-circle' : 'circle'}
            size={13}
            color={voice?.engine === 'elevenlabs' ? theme.colors.primary : theme.colors.textFaint}
          />
          <Txt variant="mono" tone={voice?.engine === 'elevenlabs' ? 'default' : 'faint'}>
            {voice?.engine === 'elevenlabs'
              ? `ElevenLabs · ${voice.model ?? 'modèle par défaut'}`
              : 'ElevenLabs · inactif (aucune clé sur le serveur)'}
          </Txt>
        </View>

        <Txt variant="small" tone="muted">
          {voice?.engine === 'elevenlabs'
            ? 'Raoul parle avec une voix de synthèse fluide, générée par le serveur. La voix du téléphone ne sert que si le réseau lâche.'
            : 'Renseigne ELEVENLABS_API_KEY côté serveur pour une voix nettement moins synthétique. En attendant, Raoul utilise la voix du téléphone.'}
        </Txt>

        <Divider />

        <SectionLabel>Voix système (repli)</SectionLabel>
        {voices === null ? (
          <Txt variant="small" tone="faint">
            Lecture des voix disponibles…
          </Txt>
        ) : voices.length === 0 ? (
          <Txt variant="small" tone="muted">
            Aucune voix française détectée. Ajoute-en une dans Réglages → Accessibilité → Contenu
            énoncé → Voix → Français. Selon la version d’iOS, l’entrée peut s’appeler « Parole ».
          </Txt>
        ) : (
          <>
            {voices.slice(0, 4).map((v) => (
              <View key={v.label} style={styles.voiceRow}>
                <Feather
                  name={v.best ? 'check-circle' : 'circle'}
                  size={13}
                  color={v.best ? theme.colors.primary : theme.colors.textFaint}
                />
                <Txt variant="mono" tone={v.best ? 'default' : 'faint'}>
                  {v.label}
                </Txt>
              </View>
            ))}
            <Txt variant="mono" tone="faint">
              Une voix « Enhanced » ou « Premium » sonne nettement mieux qu’une « Default ».
            </Txt>
          </>
        )}
      </Card>
    </ScrollView>
  );
}

/* -------------------------------------------------------------------------- */

function Source({
  icon,
  title,
  connected,
  label,
  hint,
  error,
  children,
}: {
  icon: IconName;
  title: string;
  connected: boolean;
  label?: string;
  hint: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <View style={styles.sourceHead}>
        <View style={styles.sourceIcon}>
          <Feather name={icon} size={16} color={connected ? theme.colors.primary : theme.colors.textFaint} />
        </View>
        <View style={styles.flex}>
          <Txt variant="heading">{title}</Txt>
          <View style={styles.sourceState}>
            <StatusDot state={error ? 'warn' : connected ? 'on' : 'off'} />
            <Txt variant="mono" tone={connected ? 'success' : 'faint'}>
              {error ? 'erreur' : connected ? (label ?? 'connecté') : 'non connecté'}
            </Txt>
          </View>
        </View>
      </View>
      <Txt variant="small" tone="muted">
        {hint}
      </Txt>
      {error ? (
        <Txt variant="mono" tone="danger">
          {error}
        </Txt>
      ) : null}
      <Divider />
      {children}
    </Card>
  );
}

function GandiSource({
  connection,
  busy,
  onConnect,
  onDisconnect,
}: {
  connection?: Connection;
  busy: boolean;
  onConnect: (email: string, password: string) => void;
  onDisconnect: () => void;
}) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const connected = connection?.status === 'connected';

  return (
    <Source
      icon="mail"
      title="Mails Gandi"
      connected={connected}
      label={connection?.label}
      error={connection?.last_error}
      hint="Connexion IMAP. Seuls l’expéditeur, l’objet et la date sont lus — jamais le corps des messages."
    >
      {connected ? (
        <Button label="Déconnecter" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field
            label="Adresse"
            placeholder="moi@mondomaine.fr"
            value={email}
            onChangeText={setEmail}
            keyboardType="email-address"
            textContentType="emailAddress"
          />
          <Field
            label="Mot de passe d’application"
            placeholder="••••••••"
            value={password}
            onChangeText={setPassword}
            secureTextEntry
            textContentType="password"
            hint="Gandi Admin › ta boîte mail › Mots de passe d’application."
          />
          <Button
            label="Connecter"
            icon="link"
            loading={busy}
            disabled={!email.trim() || !password}
            onPress={() => onConnect(email.trim(), password)}
          />
        </>
      )}
    </Source>
  );
}

function SlackSource({
  connection,
  busy,
  onAuthorize,
  onConnect,
  onDisconnect,
}: {
  connection?: Connection;
  busy: boolean;
  onAuthorize: () => void;
  onConnect: (token: string) => void;
  onDisconnect: () => void;
}) {
  const [token, setToken] = useState('');
  const [manual, setManual] = useState(false);
  const connected = connection?.status === 'connected';

  return (
    <Source
      icon="hash"
      title="Slack"
      connected={connected}
      label={connection?.label}
      error={connection?.last_error}
      hint="Raoul lit ce que tu n’as pas lu, avec tes propres droits. Il n’écrit jamais dans Slack."
    >
      {connected ? (
        <Button label="Déconnecter" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : manual ? (
        <>
          <Field
            label="Token utilisateur"
            placeholder="xoxp-…"
            value={token}
            onChangeText={setToken}
            secureTextEntry
            hint="Un token bot (xoxb-) ne sait pas ce que tu n’as pas lu."
          />
          <Button label="Connecter" icon="link" loading={busy} onPress={() => onConnect(token.trim())} />
          <Button label="Revenir à l’autorisation" variant="ghost" onPress={() => setManual(false)} />
        </>
      ) : (
        <>
          <Button label="Autoriser avec Slack" icon="external-link" loading={busy} onPress={onAuthorize} />
          <Button label="Coller un token" variant="ghost" onPress={() => setManual(true)} />
        </>
      )}
    </Source>
  );
}

function WhatsAppSource({
  connection,
  busy,
  onConnect,
  onDisconnect,
}: {
  connection?: Connection;
  busy: boolean;
  onConnect: (phoneId: string, token: string) => void;
  onDisconnect: () => void;
}) {
  const [phoneId, setPhoneId] = useState('');
  const [token, setToken] = useState('');
  const connected = connection?.status === 'connected';

  return (
    <Source
      icon="message-circle"
      title="WhatsApp Business"
      connected={connected}
      label={connection?.label}
      error={connection?.last_error}
      hint="API Cloud officielle. Elle ne donne aucun historique : Raoul voit les messages reçus à partir du branchement du webhook."
    >
      {connected ? (
        <Button label="Déconnecter" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field label="Phone number ID" placeholder="1234567890" value={phoneId} onChangeText={setPhoneId} />
          <Field
            label="Token d’accès permanent"
            placeholder="••••••••"
            value={token}
            onChangeText={setToken}
            secureTextEntry
          />
          <Button
            label="Connecter"
            icon="link"
            loading={busy}
            disabled={!phoneId.trim() || !token.trim()}
            onPress={() => onConnect(phoneId.trim(), token.trim())}
          />
        </>
      )}
    </Source>
  );
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
  sourceHead: { flexDirection: 'row', alignItems: 'center', gap: theme.space.md },
  sourceIcon: {
    width: 36,
    height: 36,
    borderRadius: theme.radius.sm,
    backgroundColor: theme.colors.surfaceRaised,
    alignItems: 'center',
    justifyContent: 'center',
  },
  sourceState: { flexDirection: 'row', alignItems: 'center', gap: theme.space.xs },
  voiceRow: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
});
