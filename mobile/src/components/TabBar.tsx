import React, { useEffect, useRef, useState } from 'react';
import { Animated, Easing, Pressable, StyleSheet, Text, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import * as Haptics from 'expo-haptics';

import { Glass } from './glass';
import { alpha, theme } from '../theme';

export type TabItem<K extends string> = {
  key: K;
  label: string;
  icon: React.ComponentProps<typeof Feather>['name'];
};

const BAR = 64;

/**
 * Place à réserver sous le contenu d'un écran, zone sûre comprise.
 *
 * Une barre flottante recouvre ce qui passe dessous : sans cette réserve, la
 * dernière ligne d'une liste est illisible et le dernier bouton intouchable.
 * Le calcul vit ici, avec la barre — deux écrans qui l'estiment chacun de leur
 * côté finissent par ne pas s'arrêter à la même hauteur.
 */
export function tabBarSpace(inset: number): number {
  return Math.max(inset, theme.space.md) + BAR + theme.space.sm;
}

type Props<K extends string> = {
  tabs: readonly TabItem<K>[];
  active: K;
  onChange: (key: K) => void;
  /** Marge basse rendue par la zone sûre de l'appareil. */
  inset: number;
};

/**
 * Barre d'onglets flottante.
 *
 * Elle ne touche aucun bord : c'est une plaque de verre posée au-dessus du
 * contenu, qui laisse voir la liste défiler dessous. Le curseur glisse d'un
 * onglet à l'autre plutôt que d'apparaître à sa nouvelle place — le glissement
 * dit d'où l'on vient, ce qu'un changement instantané ne dit pas.
 *
 * Icône ET libellé sur chaque onglet : une barre d'icônes seules se devine au
 * lieu de se lire, et ce qui se devine se devine mal.
 */
export function TabBar<K extends string>({ tabs, active, onChange, inset }: Props<K>) {
  const [width, setWidth] = useState(0);
  const slide = useRef(new Animated.Value(0)).current;
  const index = Math.max(
    0,
    tabs.findIndex((t) => t.key === active),
  );

  useEffect(() => {
    Animated.timing(slide, {
      toValue: index,
      duration: theme.motion.base,
      easing: Easing.bezier(...theme.motion.easing),
      useNativeDriver: true,
    }).start();
  }, [index, slide]);

  const slot = width / tabs.length;

  return (
    <View
      style={[styles.wrap, { bottom: Math.max(inset, theme.space.md) }]}
      pointerEvents="box-none"
    >
      <Glass
        variant="chrome"
        radius={theme.radius.xl}
        style={styles.bar}
        onLayout={(e) => setWidth(e.nativeEvent.layout.width)}
        accessibilityRole="tablist"
      >
        {/* Curseur : une lueur teal sous l'onglet actif, qui glisse. */}
        {width > 0 ? (
          <Animated.View
            pointerEvents="none"
            style={[
              styles.cursor,
              {
                width: slot - theme.space.sm,
                transform: [
                  {
                    translateX: slide.interpolate({
                      inputRange: [0, Math.max(tabs.length - 1, 1)],
                      outputRange: [theme.space.xs, slot * (tabs.length - 1) + theme.space.xs],
                    }),
                  },
                ],
              },
            ]}
          >
            <LinearGradient
              colors={[alpha(theme.colors.primary, 0.26), alpha(theme.colors.primary, 0.08)]}
              start={{ x: 0.5, y: 0 }}
              end={{ x: 0.5, y: 1 }}
              style={styles.cursorFill}
            />
          </Animated.View>
        ) : null}

        {tabs.map((t) => {
          const on = t.key === active;
          return (
            <Pressable
              key={t.key}
              onPress={() => {
                if (on) return;
                void Haptics.selectionAsync();
                onChange(t.key);
              }}
              accessibilityRole="tab"
              accessibilityLabel={t.label}
              accessibilityState={{ selected: on }}
              style={styles.tab}
            >
              <Feather
                name={t.icon}
                size={19}
                color={on ? theme.colors.primary : theme.colors.textFaint}
              />
              <Text style={[styles.label, on && styles.labelOn]} numberOfLines={1}>
                {t.label}
              </Text>
            </Pressable>
          );
        })}
      </Glass>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: {
    position: 'absolute',
    left: theme.space.lg,
    right: theme.space.lg,
  },
  bar: {
    flexDirection: 'row',
    height: BAR,
    alignItems: 'center',
  },
  cursor: {
    position: 'absolute',
    top: theme.space.sm,
    bottom: theme.space.sm,
    left: 0,
    borderRadius: theme.radius.lg,
    overflow: 'hidden',
  },
  cursorFill: { flex: 1 },
  tab: {
    flex: 1,
    height: '100%',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 3,
  },
  label: {
    fontFamily: theme.type.label.font,
    fontSize: 11,
    color: theme.colors.textFaint,
  },
  labelOn: { color: theme.colors.primary },
});
