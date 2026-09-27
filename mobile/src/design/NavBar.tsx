import React from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { tokens } from './tokens';
import type { IconName } from './ui';

export type NavItem<K extends string> = { key: K; label: string; icon: IconName; badge?: number };

const BAR = 58;

/** Place à réserver sous un écran, zone sûre comprise. */
export function navBarSpace(inset: number): number {
  return BAR + inset + tokens.space.md;
}

/**
 * La barre de navigation : ancrée en bas, un trait au-dessus, quatre entrées.
 *
 * Icône ET libellé, toujours : une barre d'icônes seules se devine, et ce qui
 * se devine se devine mal. L'entrée active porte l'accent et une réglette.
 */
export function NavBar<K extends string>({
  items,
  active,
  onChange,
  inset,
}: {
  items: readonly NavItem<K>[];
  active: K;
  onChange: (key: K) => void;
  inset: number;
}) {
  return (
    <View style={[styles.bar, { paddingBottom: inset, height: BAR + inset }]} accessibilityRole="tablist">
      {items.map((item) => {
        const on = item.key === active;
        const color = on ? tokens.colors.accent : tokens.colors.text3;
        return (
          <Pressable
            key={item.key}
            onPress={() => {
              if (on) return;
              void Haptics.selectionAsync();
              onChange(item.key);
            }}
            accessibilityRole="tab"
            accessibilityLabel={item.label}
            accessibilityState={{ selected: on }}
            style={styles.item}
          >
            <View style={[styles.rule, on && styles.ruleOn]} />
            <View>
              <Feather name={item.icon} size={20} color={color} />
              {item.badge ? <View style={styles.badge} /> : null}
            </View>
            <Text style={[tokens.type.label, { color, textTransform: 'uppercase' }]} numberOfLines={1}>
              {item.label}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
}

const styles = StyleSheet.create({
  bar: {
    flexDirection: 'row',
    backgroundColor: tokens.colors.bgDeep,
    borderTopWidth: 1,
    borderTopColor: tokens.colors.line,
  },
  item: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: 5 },
  rule: { position: 'absolute', top: 0, height: 2, width: 28, backgroundColor: 'transparent', borderRadius: 1 },
  ruleOn: { backgroundColor: tokens.colors.accent },
  badge: {
    position: 'absolute',
    top: -2,
    right: -4,
    width: 7,
    height: 7,
    borderRadius: 4,
    backgroundColor: tokens.colors.raoul,
  },
});
