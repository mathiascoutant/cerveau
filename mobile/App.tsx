import React, { useEffect, useState } from 'react';
import { ActivityIndicator, AppState, Pressable, StyleSheet, Text, View } from 'react-native';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';

import { AssistantScreen } from './src/screens/AssistantScreen';
import { ConnectionsScreen } from './src/screens/ConnectionsScreen';
import { openSession } from './src/api';
import { syncCalendar } from './src/lib/calendar';
import { theme } from './src/theme';

type Tab = 'raoul' | 'acces';

export default function App() {
  const [tab, setTab] = useState<Tab>('raoul');
  const [ready, setReady] = useState(false);
  const [fatal, setFatal] = useState<string | null>(null);

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

  if (!ready) {
    return (
      <View style={styles.splash}>
        <ActivityIndicator color={theme.colors.accent} size="large" />
      </View>
    );
  }

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <SafeAreaView style={styles.root} edges={['top', 'bottom']}>
        {fatal && tab === 'raoul' ? (
          <View style={styles.fatal}>
            <Text style={styles.fatalTitle}>Serveur injoignable</Text>
            <Text style={styles.fatalBody}>{fatal}</Text>
            <Text style={styles.fatalBody}>
              Renseigne l'adresse de ton VPS dans l'onglet Accès.
            </Text>
          </View>
        ) : tab === 'raoul' ? (
          <AssistantScreen />
        ) : (
          <ConnectionsScreen />
        )}

        <View style={styles.tabbar}>
          <TabButton label="Raoul" active={tab === 'raoul'} onPress={() => setTab('raoul')} />
          <TabButton label="Accès" active={tab === 'acces'} onPress={() => setTab('acces')} />
        </View>
      </SafeAreaView>
    </SafeAreaProvider>
  );
}

function TabButton({
  label,
  active,
  onPress,
}: {
  label: string;
  active: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable onPress={onPress} style={[styles.tab, active && styles.tabActive]}>
      <Text style={[styles.tabLabel, active && styles.tabLabelActive]}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: theme.colors.bg },
  splash: {
    flex: 1,
    backgroundColor: theme.colors.bg,
    alignItems: 'center',
    justifyContent: 'center',
  },
  fatal: { flex: 1, padding: theme.space(3), justifyContent: 'center', gap: theme.space(1) },
  fatalTitle: { color: theme.colors.text, fontSize: 22, fontWeight: '700' },
  fatalBody: { color: theme.colors.textMuted, fontSize: 15, lineHeight: 21 },
  tabbar: {
    flexDirection: 'row',
    gap: theme.space(1),
    paddingHorizontal: theme.space(2.5),
    paddingTop: theme.space(1),
    borderTopWidth: 1,
    borderTopColor: theme.colors.border,
  },
  tab: {
    flex: 1,
    paddingVertical: theme.space(1.5),
    borderRadius: theme.radius.pill,
    alignItems: 'center',
  },
  tabActive: { backgroundColor: theme.colors.surface },
  tabLabel: { color: theme.colors.textMuted, fontSize: 15, fontWeight: '600' },
  tabLabelActive: { color: theme.colors.text },
});
