import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Animated, AppState, Easing, Linking, StyleSheet, View } from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider, SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { useFonts, Inter_400Regular, Inter_500Medium, Inter_600SemiBold, Inter_700Bold } from '@expo-google-fonts/inter';

import { RaoulScreen } from './src/screens/RaoulScreen';
import { JobsScreen } from './src/screens/JobsScreen';
import { CSPScreen, CSPTicketScreen } from './src/screens/CSPScreen';
import { DIGEST_KEY, loadDigest, loadUrgent, SuiviScreen, URGENT_KEY } from './src/screens/SuiviScreen';
import { AccesScreen } from './src/screens/AccesScreen';
import { LoginScreen } from './src/screens/LoginScreen';
import { NavBar, NavItem } from './src/design/NavBar';
import { Button, Notice, T } from './src/design/ui';
import { tokens } from './src/design/tokens';
import { loadTodos } from './src/lib/todos';
import { isStale } from './src/lib/cache';
import { AuthRequiredError, CSPTicket, getApiUrl, onAuthRequired, openSession } from './src/api';
import { clearCache } from './src/lib/cache';
import { syncCalendar } from './src/lib/calendar';

type Tab = 'raoul' | 'jobs' | 'suivi' | 'acces';

const TABS: readonly NavItem<Tab>[] = [
  { key: 'raoul', label: 'Raoul', icon: 'message-square' },
  { key: 'jobs', label: 'Jobs', icon: 'briefcase' },
  { key: 'suivi', label: 'Suivi', icon: 'check-square' },
  { key: 'acces', label: 'Accès', icon: 'sliders' },
];

/**
 * Lien profond posé par le widget de l'écran d'accueil (plugins/ios/RaoulWidget).
 * Un widget ne peut pas prendre le micro — iOS le réserve aux apps au premier
 * plan — donc il ouvre l'app ici, et c'est l'app qui démarre l'écoute.
 */
const LISTEN_LINK = /^raoul:\/\/listen\/?$/i;

/** Au-delà, une liste rapportée du dernier passage n'est plus une information. */
const STALE_AFTER = 10 * 60 * 1000;

/** La pile de l'onglet Jobs : les cartes, puis la vue CSP, puis un ticket. */
type JobsRoute = { name: 'jobs' } | { name: 'csp' } | { name: 'ticket'; ticket: CSPTicket };

export default function App() {
  const [tab, setTab] = useState<Tab>('raoul');
  const [ready, setReady] = useState(false);
  const [fatal, setFatal] = useState<string | null>(null);
  const [listenRequest, setListenRequest] = useState(0);
  const [jobsStack, setJobsStack] = useState<JobsRoute[]>([{ name: 'jobs' }]);
  // L'adresse essayée, affichée sur l'écran d'attente après quelques
  // secondes : quand ça tourne, il faut savoir vers quoi.
  const [apiUrl, setApiUrl] = useState('');
  const [slow, setSlow] = useState(false);
  // login : le serveur exige un compte et cet appareil n'en a pas de session.
  const [login, setLogin] = useState<string | null>(null);

  const [fontsLoaded] = useFonts({ Inter_400Regular, Inter_500Medium, Inter_600SemiBold, Inter_700Bold });

  const boot = useCallback(() => {
    setFatal(null);
    setLogin(null);
    openSession()
      .then(() => {
        setReady(true);
        // Ce que les autres onglets afficheront part tout de suite : attendre
        // l'ouverture de l'onglet, c'est faire patienter pour un travail qui
        // aurait pu commencer trente secondes plus tôt.
        void loadDigest().catch(() => undefined);
        void loadUrgent().catch(() => undefined);
        void loadTodos().catch(() => undefined);
      })
      .catch((err: Error) => {
        if (err instanceof AuthRequiredError) setLogin(err.message);
        else setFatal(err.message);
        setReady(true);
      });
  }, []);

  useEffect(boot, [boot]);

  // Une session refusée en cours d'usage renvoie à l'écran de connexion.
  useEffect(() => {
    onAuthRequired(() => {
      clearCache();
      setLogin('Ta session a expiré, reconnecte-toi.');
    });
    return () => onAuthRequired(null);
  }, []);

  useEffect(() => {
    void getApiUrl().then(setApiUrl).catch(() => undefined);
    if (ready) return;
    const timer = setTimeout(() => setSlow(true), 3000);
    return () => clearTimeout(timer);
  }, [ready]);

  useEffect(() => {
    if (!ready || fatal) return;
    void syncCalendar().catch(() => undefined);
    const sub = AppState.addEventListener('change', (next) => {
      if (next !== 'active') return;
      void syncCalendar().catch(() => undefined);
      if (isStale(DIGEST_KEY, STALE_AFTER)) void loadDigest(true).catch(() => undefined);
      if (isStale(URGENT_KEY, STALE_AFTER)) void loadUrgent(true).catch(() => undefined);
      void loadTodos(true).catch(() => undefined);
    });
    return () => sub.remove();
  }, [ready, fatal]);

  useEffect(() => {
    const handle = (url: string | null | undefined) => {
      if (!url || !LISTEN_LINK.test(url.trim())) return;
      setTab('raoul');
      setListenRequest((n) => n + 1);
    };
    void Linking.getInitialURL().then(handle).catch(() => undefined);
    const sub = Linking.addEventListener('url', (event) => handle(event.url));
    return () => sub.remove();
  }, []);

  if (!ready || !fontsLoaded) {
    return (
      <View style={styles.splash}>
        <ActivityIndicator color={tokens.colors.accent} size="large" />
        {slow && fontsLoaded ? (
          <View style={styles.splashHint}>
            <T v="mono" tone="faint" style={styles.splashText}>
              connexion à {apiUrl || '…'}
            </T>
            <T v="small" tone="muted" style={styles.splashText}>
              Le serveur ne répond pas encore. S’il ne tourne pas à cette adresse, change-la dans Accès.
            </T>
            <Button
              label="Ouvrir Accès"
              variant="secondary"
              icon="sliders"
              compact
              onPress={() => {
                setReady(true);
                setTab('acces');
              }}
            />
          </View>
        ) : null}
      </View>
    );
  }

  if (login !== null) {
    return (
      <SafeAreaProvider>
        <StatusBar style="light" />
        <View style={styles.root}>
          <SafeAreaView style={styles.flex} edges={['top', 'bottom']}>
            <LoginScreen reason={login} onDone={() => { clearCache(); boot(); }} />
          </SafeAreaView>
        </View>
      </SafeAreaProvider>
    );
  }

  const route = jobsStack[jobsStack.length - 1];
  const push = (r: JobsRoute) => setJobsStack((s) => [...s, r]);
  const pop = () => setJobsStack((s) => (s.length > 1 ? s.slice(0, -1) : s));

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <View style={styles.root}>
        <SafeAreaView style={styles.flex} edges={['top']}>
          {fatal && tab === 'raoul' ? (
            <View style={styles.fatal}>
              <Notice tone="danger" icon="wifi-off" title="Serveur injoignable">
                <T v="small" tone="muted">
                  {fatal}
                </T>
                <T v="small" tone="muted">
                  Renseigne l’adresse de ton serveur dans Accès, ou réessaie.
                </T>
                <View style={styles.fatalActions}>
                  <Button label="Réessayer" variant="secondary" icon="refresh-cw" compact onPress={boot} />
                  <Button label="Accès" variant="ghost" icon="sliders" compact onPress={() => setTab('acces')} />
                </View>
              </Notice>
            </View>
          ) : (
            <Screen key={tab === 'jobs' ? `jobs-${route.name}` : tab}>
              {tab === 'raoul' ? (
                <RaoulScreen listenRequest={listenRequest} />
              ) : tab === 'jobs' ? (
                route.name === 'ticket' ? (
                  <CSPTicketScreen ticket={route.ticket} onBack={pop} />
                ) : route.name === 'csp' ? (
                  <CSPScreen onBack={pop} onOpen={(ticket) => push({ name: 'ticket', ticket })} onAccess={() => setTab('acces')} />
                ) : (
                  <JobsScreen onOpen={(key) => key === 'csp' && push({ name: 'csp' })} />
                )
              ) : tab === 'suivi' ? (
                <SuiviScreen />
              ) : (
                <AccesScreen
                  onLoggedOut={() => {
                    clearCache();
                    setTab('raoul');
                    setLogin('Déconnecté de cet appareil.');
                  }}
                />
              )}
            </Screen>
          )}
        </SafeAreaView>
        <Bottom tab={tab} onChange={setTab} />
      </View>
    </SafeAreaProvider>
  );
}

/** L'écran courant, fondu à chaque changement — court, pour ne pas se voir. */
function Screen({ children }: { children: React.ReactNode }) {
  const fade = useRef(new Animated.Value(0)).current;
  useEffect(() => {
    Animated.timing(fade, { toValue: 1, duration: tokens.motion.base, easing: Easing.bezier(...tokens.motion.easing), useNativeDriver: true }).start();
  }, [fade]);
  return (
    <Animated.View style={[styles.flex, { opacity: fade, transform: [{ translateY: fade.interpolate({ inputRange: [0, 1], outputRange: [8, 0] }) }] }]}>
      {children}
    </Animated.View>
  );
}

function Bottom({ tab, onChange }: { tab: Tab; onChange: (t: Tab) => void }) {
  const insets = useSafeAreaInsets();
  return <NavBar items={TABS} active={tab} onChange={onChange} inset={insets.bottom} />;
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: tokens.colors.bg },
  flex: { flex: 1 },
  splash: { flex: 1, backgroundColor: tokens.colors.bg, alignItems: 'center', justifyContent: 'center', gap: tokens.space.xl, padding: tokens.space.xl },
  splashHint: { alignItems: 'center', gap: tokens.space.md },
  splashText: { textAlign: 'center' },
  fatal: { flex: 1, justifyContent: 'center', padding: tokens.space.xl },
  fatalActions: { flexDirection: 'row', gap: tokens.space.sm, marginTop: tokens.space.sm },
});
