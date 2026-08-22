import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  AppState,
  Linking,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';
import { Feather } from '@expo/vector-icons';
import {
  useFonts,
  Inter_400Regular,
  Inter_500Medium,
  Inter_600SemiBold,
  Inter_700Bold,
} from '@expo-google-fonts/inter';

import { AssistantScreen } from './src/screens/AssistantScreen';
import { JournalScreen } from './src/screens/JournalScreen';
import { DraftsScreen } from './src/screens/DraftsScreen';
import { ConnectionsScreen } from './src/screens/ConnectionsScreen';
import { Banner, Txt } from './src/components/ui';
import { openSession } from './src/api';
import { syncCalendar } from './src/lib/calendar';
import { theme } from './src/theme';

type Tab = 'raoul' | 'journal' | 'reponses' | 'acces';

const TABS: { key: Tab; label: string; icon: React.ComponentProps<typeof Feather>['name'] }[] = [
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
  useEffect(() => {
    openSession()
      .then(() => setReady(true))
      .catch((err: Error) => {
        setFatal(err.message);
        setReady(true);
      });
  }, []);

  // Le miroir d'agenda doit rester frais : on resynchronise à chaque retour
  // au premier plan.
  useEffect(() => {
    if (!ready || fatal) return;
    void syncCalendar().catch(() => undefined);
    const sub = AppState.addEventListener('change', (next) => {
      if (next === 'active') void syncCalendar().catch(() => undefined);
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
        <ActivityIndicator color={theme.colors.primary} size="large" />
      </View>
    );
  }

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <SafeAreaView style={styles.root} edges={['top', 'bottom']}>
        <View style={styles.body}>
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
          ) : tab === 'raoul' ? (
            <AssistantScreen listenRequest={listenRequest} />
          ) : tab === 'journal' ? (
            <JournalScreen />
          ) : tab === 'reponses' ? (
            <DraftsScreen />
          ) : (
            <ConnectionsScreen />
          )}
        </View>

        <View style={styles.tabbar} accessibilityRole="tablist">
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <Pressable
                key={t.key}
                onPress={() => setTab(t.key)}
                accessibilityRole="tab"
                accessibilityLabel={t.label}
                accessibilityState={{ selected: active }}
                style={styles.tab}
              >
                {/* Le trait supérieur double la couleur : l'onglet actif reste
                    identifiable sans distinguer les teintes. */}
                <View style={[styles.tabMarker, active && styles.tabMarkerActive]} />
                <Feather
                  name={t.icon}
                  size={19}
                  color={active ? theme.colors.primary : theme.colors.textFaint}
                />
                <Text style={[styles.tabLabel, active && styles.tabLabelActive]}>{t.label}</Text>
              </Pressable>
            );
          })}
        </View>
      </SafeAreaView>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: theme.colors.background },
  body: { flex: 1 },
  splash: {
    flex: 1,
    backgroundColor: theme.colors.background,
    alignItems: 'center',
    justifyContent: 'center',
  },
  fatal: { flex: 1, justifyContent: 'center', padding: theme.space.xl },

  tabbar: {
    flexDirection: 'row',
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: theme.colors.border,
    backgroundColor: theme.colors.surface,
    paddingTop: theme.space.xs,
  },
  tab: {
    flex: 1,
    minHeight: theme.touchMin + 8,
    alignItems: 'center',
    justifyContent: 'center',
    gap: 3,
    paddingBottom: theme.space.sm,
  },
  tabMarker: {
    position: 'absolute',
    top: -theme.space.xs,
    width: 28,
    height: 2,
    borderRadius: 1,
    backgroundColor: 'transparent',
  },
  tabMarkerActive: { backgroundColor: theme.colors.primary },
  tabLabel: {
    fontFamily: theme.type.label.font,
    fontSize: 11,
    color: theme.colors.textFaint,
  },
  tabLabelActive: { color: theme.colors.primary },
});
