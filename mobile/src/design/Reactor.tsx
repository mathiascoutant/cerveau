import React, { useEffect, useRef, useState } from 'react';
import { AccessibilityInfo, Animated, Easing, Pressable, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { alpha, tokens } from './tokens';
import type { IconName } from './ui';

export type ReactorState = 'off' | 'waiting' | 'listening' | 'thinking' | 'speaking';

type Props = {
  state: ReactorState;
  enabled: boolean;
  onPress: () => void;
  onLongPress: () => void;
  size?: number;
};

/**
 * Le réacteur : l'état de Raoul, lisible de l'autre bout de la pièce.
 *
 * Une couronne graduée qui tourne lentement, un cœur qui respire, et une
 * couleur par état — cyan quand il écoute, violet quand il réfléchit, ambre
 * quand il parle. La couleur ne porte jamais l'information seule : l'icône
 * change avec l'état, et l'écran affiche le libellé juste à côté.
 *
 * La rotation s'arrête si le système demande moins d'animations.
 */
export const REACTOR_COLOR: Record<ReactorState, string> = {
  off: tokens.colors.text3,
  waiting: tokens.colors.accentDeep,
  listening: tokens.colors.accent,
  thinking: tokens.colors.thinking,
  speaking: tokens.colors.raoul,
};

const ICON: Record<ReactorState, IconName> = {
  off: 'mic-off',
  waiting: 'mic',
  listening: 'radio',
  thinking: 'cpu',
  speaking: 'volume-2',
};

const TICKS = 48;

export function Reactor({ state, enabled, onPress, onLongPress, size = 132 }: Props) {
  const spin = useRef(new Animated.Value(0)).current;
  const breath = useRef(new Animated.Value(0)).current;
  const pulse = useRef(new Animated.Value(0)).current;
  const press = useRef(new Animated.Value(1)).current;
  const [still, setStill] = useState(false);

  const active = state !== 'off';
  const color = REACTOR_COLOR[state];

  useEffect(() => {
    let alive = true;
    void AccessibilityInfo.isReduceMotionEnabled().then((v) => alive && setStill(v));
    return () => {
      alive = false;
    };
  }, []);

  // La couronne tourne en permanence, plus vite quand Raoul travaille.
  useEffect(() => {
    if (still) return;
    spin.setValue(0);
    const loop = Animated.loop(
      Animated.timing(spin, {
        toValue: 1,
        duration: state === 'thinking' ? 6000 : state === 'off' ? 40000 : 18000,
        easing: Easing.linear,
        useNativeDriver: true,
      }),
    );
    loop.start();
    return () => loop.stop();
  }, [spin, state, still]);

  useEffect(() => {
    if (still) return;
    const loop = Animated.loop(
      Animated.sequence([
        Animated.timing(breath, { toValue: 1, duration: 2200, easing: Easing.inOut(Easing.sin), useNativeDriver: true }),
        Animated.timing(breath, { toValue: 0, duration: 2200, easing: Easing.inOut(Easing.sin), useNativeDriver: true }),
      ]),
    );
    loop.start();
    return () => loop.stop();
  }, [breath, still]);

  // Les anneaux qui s'échappent : seulement quand il écoute ou parle.
  const pulsing = (state === 'listening' || state === 'speaking' || state === 'waiting') && !still;
  useEffect(() => {
    if (!pulsing) {
      pulse.stopAnimation();
      pulse.setValue(0);
      return;
    }
    const loop = Animated.loop(
      Animated.timing(pulse, {
        toValue: 1,
        duration: state === 'listening' ? 1500 : 2400,
        easing: Easing.out(Easing.ease),
        useNativeDriver: true,
      }),
    );
    loop.start();
    return () => loop.stop();
  }, [pulse, pulsing, state]);

  const rotate = spin.interpolate({ inputRange: [0, 1], outputRange: ['0deg', '360deg'] });
  const coreScale = breath.interpolate({ inputRange: [0, 1], outputRange: [1, active ? 1.07 : 1.02] });
  const ringStyle = (delay: number) => ({
    opacity: pulse.interpolate({ inputRange: [0, delay, Math.min(delay + 0.6, 1), 1], outputRange: [0, 0.45, 0, 0] }),
    transform: [{ scale: pulse.interpolate({ inputRange: [0, delay, 1], outputRange: [0.9, 0.98, 1.6] }) }],
  });

  const label = state === 'off' ? "Activer l'écoute" : "Couper l'écoute. Appui long pour parler tout de suite.";
  const core = size * 0.56;
  const disabled = !enabled && state === 'off';

  return (
    <View style={[styles.wrap, { width: size * 1.4, height: size * 1.4 }]}>
      <View
        style={[styles.halo, { width: size * 1.4, height: size * 1.4, borderRadius: size * 0.7, backgroundColor: alpha(color, active ? 0.08 : 0.03) }]}
        pointerEvents="none"
      />
      {pulsing ? (
        <>
          <Animated.View style={[styles.ring, { width: size, height: size, borderRadius: size / 2, borderColor: color }, ringStyle(0)]} pointerEvents="none" />
          <Animated.View style={[styles.ring, { width: size, height: size, borderRadius: size / 2, borderColor: color }, ringStyle(0.4)]} pointerEvents="none" />
        </>
      ) : null}

      {/* La couronne graduée. */}
      <Animated.View style={[styles.crown, { width: size, height: size, transform: [{ rotate }] }]} pointerEvents="none">
        {Array.from({ length: TICKS }).map((_, i) => {
          const long = i % 6 === 0;
          return (
            <View
              key={i}
              style={[
                styles.tickSlot,
                { width: size, height: size, transform: [{ rotate: `${(360 / TICKS) * i}deg` }] },
              ]}
            >
              <View
                style={{
                  width: long ? 2 : 1,
                  height: long ? 10 : 5,
                  backgroundColor: alpha(color, long ? 0.85 : 0.4),
                }}
              />
            </View>
          );
        })}
      </Animated.View>
      <View style={[styles.outline, { width: size - 24, height: size - 24, borderRadius: (size - 24) / 2, borderColor: alpha(color, 0.35) }]} pointerEvents="none" />

      <Animated.View style={{ transform: [{ scale: press }] }}>
        <Pressable
          onPress={() => {
            void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Medium);
            onPress();
          }}
          onLongPress={() => {
            void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Heavy);
            onLongPress();
          }}
          onPressIn={() => Animated.spring(press, { toValue: 0.94, useNativeDriver: true, ...tokens.motion.spring }).start()}
          onPressOut={() => Animated.spring(press, { toValue: 1, useNativeDriver: true, ...tokens.motion.spring }).start()}
          disabled={disabled}
          accessibilityRole="button"
          accessibilityLabel={label}
          accessibilityState={{ disabled }}
        >
          <Animated.View
            style={[
              styles.core,
              {
                width: core,
                height: core,
                borderRadius: core / 2,
                borderColor: alpha(color, active ? 0.7 : 0.3),
                backgroundColor: alpha(color, active ? 0.16 : 0.05),
                transform: [{ scale: coreScale }],
              },
            ]}
          >
            <Feather name={ICON[state]} size={core * 0.36} color={color} />
          </Animated.View>
        </Pressable>
      </Animated.View>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { alignItems: 'center', justifyContent: 'center', alignSelf: 'center' },
  halo: { position: 'absolute' },
  ring: { position: 'absolute', borderWidth: 1 },
  crown: { position: 'absolute' },
  tickSlot: { position: 'absolute', alignItems: 'center', justifyContent: 'flex-start' },
  outline: { position: 'absolute', borderWidth: 1 },
  core: { alignItems: 'center', justifyContent: 'center', borderWidth: 1.5 },
});
