import React, { useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Animated, AppState, Easing, Linking, StyleSheet, View } from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider, SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import {
  useFonts,
  Inter_400Regular,
  Inter_500Medium,
  Inter_600SemiBold,
  Inter_700Bold,
} from '@expo-google-fonts/inter';

import { AssistantScreen } from './src/screens/AssistantScreen';
import { DIGEST_KEY, JournalScreen, loadJournal } from './src/screens/JournalScreen';
import { DraftsScreen } from './src/screens/DraftsScreen';
import { ConnectionsScreen } from './src/screens/ConnectionsScreen';
import { Banner, Txt } from './src/components/ui';
import { Backdrop } from './src/components/glass';
import { TabBar, TabItem } from './src/components/TabBar';
import { loadUrgent, URGENT_KEY } from './src/components/Urgences';
import { loadTodos, TODOS_KEY } from './src/lib/todos';
import { isStale } from './src/lib/cache';
import { openSession } from './src/api';
import { syncCalendar } from './src/lib/calendar';
import { theme } from './src/theme';

type Tab = 'raoul' | 'journal' | 'reponses' | 'acces';

const TABS: readonly TabItem<Tab>[] = [
  { key: 'raoul', label: 'Raoul', icon: 'mic' },
  { key: 'journal', label: 'Journal', icon: 'layout' },
  { key: 'reponses', label: 'Réponses', icon: 'edit-3' },
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

export default function App() {
  const [tab, setTab] = useState<Tab>('raoul');
  const [ready, setReady] = useState(false);
  const [fatal, setFatal] = useState<string | null>(null);
  // Compteur plutôt que booléen : deux appuis successifs sur le widget doivent
  // relancer l'écoute deux fois, or un booléen déjà vrai ne rejoue pas d'effet.
  const [listenRequest, setListenRequest] = useState(0);

  const [fontsLoaded] = useFonts({
    Inter_400Regular,
    Inter_500Medium,
    Inter_600SemiBold,
    Inter_700Bold,
  });

  // Aucun écran de connexion : on ouvre la session avec l'identifiant
  // d'appareil dès le lancement, et on entre directement dans l'app.
  //
  // Dès que la session est ouverte, on lance en fond ce que les autres onglets
  // afficheront. La synthèse du Journal et la liste à traiter demandent toutes
  // deux le modèle : les attendre à l'ouverture de leur onglet, c'est faire
  // patienter pour un travail qui aurait pu commencer trente secondes plus tôt.
  useEffect(() => {
    openSession()
      .then(() => {
        setReady(true);
        void loadJournal().catch(() => undefined);
        void loadUrgent().catch(() => undefined);
        void loadTodos().catch(() => undefined);
      })
      .catch((err: Error) => {
        setFatal(err.message);
        setReady(true);
      });
  }, []);

  // Le miroir d'agenda doit rester frais : on resynchronise à chaque retour
  // au premier plan. Les listes suivent, mais seulement si elles ont vieilli —
  // rouvrir l'app trente secondes après l'avoir fermée ne justifie pas de
  // redemander une synthèse au modèle.
  useEffect(() => {
    if (!ready || fatal) return;
    void syncCalendar().catch(() => undefined);
    const sub = AppState.addEventListener('change', (next) => {
      if (next !== 'active') return;
      void syncCalendar().catch(() => undefined);
      if (isStale(DIGEST_KEY, STALE_AFTER)) void loadJournal(true).catch(() => undefined);
      if (isStale(URGENT_KEY, STALE_AFTER)) void loadUrgent(true).catch(() => undefined);
      // La liste à faire ne coûte pas d'appel au modèle, et elle peut avoir
      // bougé depuis un autre appareil : on la relit à chaque retour.
      void loadTodos(true).catch(() => undefined);
    });
    return () => sub.remove();
  }, [ready, fatal]);

  // Le widget ouvre « raoul://listen ». Deux chemins à couvrir : l'app était
  // fermée (getInitialURL) ou déjà en fond (événement « url »).
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
        <Backdrop />
        <ActivityIndicator color={theme.colors.primary} size="large" />
      </View>
    );
  }

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <View style={styles.root}>
        {/* Le fond est peint une fois pour toute l'app. Les écrans qui
            défilent ne le repeignent pas, sinon le flou n'a plus rien à
            flouter et les cartes redeviennent des rectangles gris. */}
        <Backdrop />

        <SafeAreaView style={styles.flex} edges={['top']}>
          {fatal && tab === 'raoul' ? (
            <View style={styles.fatal}>
              <Banner tone="danger" icon="wifi-off">
                <Txt variant="bodyStrong" tone="danger">
                  Serveur injoignable
                </Txt>
                <Txt variant="small" tone="muted">
                  {fatal}
                </Txt>
                <Txt variant="small" tone="muted">
                  Renseigne l’adresse de ton serveur dans l’onglet Accès.
                </Txt>
              </Banner>
            </View>
          ) : (
            <Screen tab={tab} listenRequest={listenRequest} />
          )}
        </SafeAreaView>

        <FloatingTabs tab={tab} onChange={setTab} />
      </View>
    </SafeAreaProvider>
  );
}

/**
 * L'écran courant, fondu à chaque changement d'onglet.
 *
 * Le fondu est court et léger — 240 ms, dix pixels de montée. Il ne cherche pas
 * à impressionner : il évite la coupure sèche entre deux écrans qui n'ont ni le
 * même contenu ni la même hauteur.
 */
function Screen({ tab, listenRequest }: { tab: Tab; listenRequest: number }) {
  const fade = useRef(new Animated.Value(1)).current;

  useEffect(() => {
    fade.setValue(0);
    Animated.timing(fade, {
      toValue: 1,
      duration: theme.motion.base,
      easing: Easing.bezier(...theme.motion.easing),
      useNativeDriver: true,
    }).start();
  }, [tab, fade]);

  return (
    <Animated.View
      style={[
        styles.flex,
        { opacity: fade, transform: [{ translateY: fade.interpolate({ inputRange: [0, 1], outputRange: [10, 0] }) }] },
      ]}
    >
      {tab === 'raoul' ? (
        <AssistantScreen listenRequest={listenRequest} />
      ) : tab === 'journal' ? (
        <JournalScreen />
      ) : tab === 'reponses' ? (
        <DraftsScreen />
      ) : (
        <ConnectionsScreen />
      )}
    </Animated.View>
  );
}

/** La barre a besoin de la zone sûre, qui n'est lisible que sous le provider. */
function FloatingTabs({ tab, onChange }: { tab: Tab; onChange: (t: Tab) => void }) {
  const insets = useSafeAreaInsets();
  return <TabBar tabs={TABS} active={tab} onChange={onChange} inset={insets.bottom} />;
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: theme.colors.background },
  flex: { flex: 1 },
  splash: {
    flex: 1,
    backgroundColor: theme.colors.background,
    alignItems: 'center',
    justifyContent: 'center',
  },
  fatal: { flex: 1, justifyContent: 'center', padding: theme.space.xl },
});
