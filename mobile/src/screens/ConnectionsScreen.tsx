import React, { useCallback, useEffect, useState } from 'react';
import { Alert, ScrollView, StyleSheet, Text, View } from 'react-native';

import { Button, Card, Field, Label, StatusDot, Subtitle, Title } from '../components/ui';
import { api, Connection, detectedApiUrl, getApiUrl, Provider, setApiUrl } from '../api';
import {
  calendarSupported,
  hasCalendarAccess,
  requestCalendarAccess,
  syncCalendar,
} from '../lib/calendar';
import { theme } from '../theme';

export function ConnectionsScreen() {
  const [connections, setConnections] = useState<Record<string, Connection>>({});
  const [serverUrl, setServerUrl] = useState('');
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
      <Title>Accès</Title>
      <Subtitle>
        Raoul ne peut répondre que sur ce à quoi tu lui donnes accès. Tout est chiffré côté serveur
        et rien n'est stocké sur le téléphone.
      </Subtitle>

      <Card>
        <Label>Serveur</Label>
        <Field
          placeholder="https://cerveau.mondomaine.fr"
          value={serverUrl}
          onChangeText={setServerUrl}
          keyboardType="url"
        />
        <Subtitle>
          En développement, l'adresse est déduite de la machine qui sert le bundle Expo :{' '}
          {detectedApiUrl()}
        </Subtitle>
        <Button
          label="Enregistrer l'adresse"
          variant="ghost"
          loading={busy === 'server'}
          onPress={() =>
            run('server', async () => {
              await setApiUrl(serverUrl);
              await api.status();
            })
          }
        />
      </Card>

      <CalendarSection
        ready={calendarReady}
        usable={calendarUsable}
        busy={busy === 'calendar'}
        onConnect={() =>
          run('calendar', async () => {
            const granted = await requestCalendarAccess();
            if (!granted) throw new Error("Accès au calendrier refusé dans les réglages iOS.");
            const count = await syncCalendar();
            Alert.alert('Agenda synchronisé', `${count} événements envoyés à Raoul.`);
          })
        }
      />

      <GandiSection
        connection={connections.gandi}
        busy={busy === 'gandi'}
        onConnect={(email, password) =>
          run('gandi', async () => {
            await api.connectGandi(email, password);
          })
        }
        onDisconnect={() => disconnect('gandi')}
      />

      <SlackSection
        connection={connections.slack}
        busy={busy === 'slack'}
        onConnect={(token) =>
          run('slack', async () => {
            await api.connectSlack(token);
          })
        }
        onDisconnect={() => disconnect('slack')}
      />

      <WhatsAppSection
        connection={connections.whatsapp}
        busy={busy === 'whatsapp'}
        onConnect={(phoneId, token) =>
          run('whatsapp', async () => {
            await api.connectWhatsApp(phoneId, token);
          })
        }
        onDisconnect={() => disconnect('whatsapp')}
      />
    </ScrollView>
  );
}

function SectionHeader({
  title,
  connection,
  ready,
  hint,
}: {
  title: string;
  connection?: Connection;
  ready?: boolean;
  hint: string;
}) {
  const connected = ready ?? connection?.status === 'connected';
  return (
    <>
      <View style={styles.row}>
        <StatusDot ok={connected} warn={connection?.status === 'error'} />
        <Text style={styles.sectionTitle}>{title}</Text>
        {connection?.label && <Text style={styles.badge}>{connection.label}</Text>}
      </View>
      <Subtitle>{hint}</Subtitle>
      {connection?.last_error && <Text style={styles.error}>{connection.last_error}</Text>}
    </>
  );
}

function CalendarSection({
  ready,
  usable,
  busy,
  onConnect,
}: {
  ready: boolean;
  usable: boolean;
  busy: boolean;
  onConnect: () => void;
}) {
  return (
    <Card>
      <SectionHeader
        title="Agenda iOS"
        ready={ready}
        hint="Raoul lit tous les calendriers du téléphone — iCloud, Google, Exchange — et y écrit les créneaux qu'il valide."
      />
      {usable ? (
        <Button
          label={ready ? "Resynchroniser l'agenda" : "Autoriser l'accès au calendrier"}
          variant={ready ? 'ghost' : 'primary'}
          loading={busy}
          onPress={onConnect}
        />
      ) : (
        <Text style={styles.notice}>
          Indisponible dans Expo Go : expo-calendar est un module natif. Disponible dès le dev build.
        </Text>
      )}
    </Card>
  );
}

function GandiSection({
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
    <Card>
      <SectionHeader
        title="Mails Gandi"
        connection={connection}
        hint="Connexion IMAP. Gandi n'a pas d'OAuth pour le mail : il faut un mot de passe d'application (Gandi Admin › Boîte mail › Mots de passe d'application)."
      />
      {connected ? (
        <Button label="Déconnecter" variant="danger" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field
            placeholder="moi@mondomaine.fr"
            value={email}
            onChangeText={setEmail}
            keyboardType="email-address"
          />
          <Field
            placeholder="Mot de passe d'application"
            value={password}
            onChangeText={setPassword}
            secureTextEntry
          />
          <Button
            label="Connecter la boîte mail"
            loading={busy}
            onPress={() => onConnect(email.trim(), password)}
          />
        </>
      )}
    </Card>
  );
}

function SlackSection({
  connection,
  busy,
  onConnect,
  onDisconnect,
}: {
  connection?: Connection;
  busy: boolean;
  onConnect: (token: string) => void;
  onDisconnect: () => void;
}) {
  const [token, setToken] = useState('');
  const connected = connection?.status === 'connected';

  return (
    <Card>
      <SectionHeader
        title="Slack"
        connection={connection}
        hint="Token utilisateur (xoxp-…), pas un bot token : seul un token utilisateur sait ce que TU n'as pas lu."
      />
      {connected ? (
        <Button label="Déconnecter" variant="danger" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field placeholder="xoxp-…" value={token} onChangeText={setToken} secureTextEntry />
          <Button label="Connecter Slack" loading={busy} onPress={() => onConnect(token.trim())} />
        </>
      )}
    </Card>
  );
}

function WhatsAppSection({
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
    <Card>
      <SectionHeader
        title="WhatsApp Business"
        connection={connection}
        hint="API Cloud officielle de Meta. Elle ne donne pas l'historique : Raoul voit les messages reçus à partir du branchement du webhook."
      />
      {connected ? (
        <Button label="Déconnecter" variant="danger" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field placeholder="Phone number ID" value={phoneId} onChangeText={setPhoneId} />
          <Field
            placeholder="Token d'accès permanent"
            value={token}
            onChangeText={setToken}
            secureTextEntry
          />
          <Button
            label="Connecter WhatsApp"
            loading={busy}
            onPress={() => onConnect(phoneId.trim(), token.trim())}
          />
        </>
      )}
    </Card>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: theme.colors.bg },
  content: { padding: theme.space(2.5), paddingBottom: theme.space(6), gap: theme.space(2) },
  row: { flexDirection: 'row', alignItems: 'center', gap: theme.space(1) },
  sectionTitle: { color: theme.colors.text, fontSize: 17, fontWeight: '700' },
  badge: { color: theme.colors.textMuted, fontSize: 12, flexShrink: 1 },
  error: { color: theme.colors.danger, fontSize: 12 },
  notice: { color: theme.colors.warning, fontSize: 13, lineHeight: 19 },
});
