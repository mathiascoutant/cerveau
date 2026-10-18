import React from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { alpha, tokens } from './tokens';
import type { IconName } from './ui';

export type SpaceTab<K extends string> = { key: K; label: string; icon: IconName };

/**
 * La barre d'un espace : son nom, et les vues qu'il contient.
 *
 * Pro et Perso sont deux mondes qui ne se mélangent pas ; la barre le dit en
 * haut de l'écran avant même qu'on lise le contenu. Le nom de l'espace est un
 * relevé — mono, capitales — et les vues sont des segments, pas des onglets :
 * on reste dans le même espace en passant de l'une à l'autre.
 */
export function SpaceBar<K extends string>({
  space,
  tone,
  tabs,
  active,
  onChange,
}: {
  space: string;
  tone: string;
  tabs: readonly SpaceTab<K>[];
  active: K;
  onChange: (key: K) => void;
}) {
  return (
    <View style={styles.bar}>
      <View style={[styles.space, { borderColor: alpha(tone, 0.45), backgroundColor: alpha(tone, 0.08) }]}>
        <Text style={[tokens.type.label, { color: tone, textTransform: 'uppercase' }]}>{space}</Text>
      </View>
      <View style={styles.segments} accessibilityRole="tablist">
        {tabs.map((tab) => {
          const on = tab.key === active;
          const color = on ? tokens.colors.text : tokens.colors.text3;
          return (
            <Pressable
              key={tab.key}
              onPress={() => {
                if (on) return;
                void Haptics.selectionAsync();
                onChange(tab.key);
              }}
              accessibilityRole="tab"
              accessibilityLabel={tab.label}
              accessibilityState={{ selected: on }}
              style={[styles.segment, on && styles.segmentOn]}
            >
              <Feather name={tab.icon} size={13} color={on ? tone : tokens.colors.text3} />
              <Text style={[tokens.type.smallStrong, styles.label, { color }]} numberOfLines={1}>
                {tab.label}
              </Text>
            </Pressable>
          );
        })}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  bar: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: tokens.space.md,
    paddingHorizontal: tokens.space.lg,
    paddingTop: tokens.space.sm,
    paddingBottom: tokens.space.xs,
  },
  space: { paddingHorizontal: 8, paddingVertical: 4, borderRadius: tokens.radius.sm, borderWidth: 1 },
  segments: {
    flex: 1,
    flexDirection: 'row',
    padding: 3,
    gap: 3,
    borderRadius: tokens.radius.md,
    borderWidth: 1,
    borderColor: tokens.colors.line,
    backgroundColor: tokens.colors.bgDeep,
  },
  segment: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 6,
    minHeight: 34,
    borderRadius: tokens.radius.sm,
  },
  segmentOn: { backgroundColor: tokens.colors.panelRaised },
  label: { flexShrink: 1 },
});
