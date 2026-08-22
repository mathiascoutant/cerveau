import React, { useEffect, useRef } from 'react';
import { AccessibilityInfo, ActivityIndicator, Animated, Easing, Pressable, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';

import { theme } from '../theme';
import type { RaoulState } from '../hooks/useRaoul';

type Props = {
  state: RaoulState;
  enabled: boolean;
  onPress: () => void;
  onLongPress: () => void;
};

const ACCENT: Record<RaoulState, string> = {
  off: theme.colors.textFaint,
  waiting: theme.colors.primary,
  listening: theme.colors.success,
  thinking: theme.colors.warning,
  speaking: theme.colors.primary,
};

const ICON: Record<RaoulState, React.ComponentProps<typeof Feather>['name']> = {
  off: 'mic-off',
  waiting: 'mic',
  listening: 'radio',
  thinking: 'loader',
  speaking: 'volume-2',
};

/**
 * L'orbe est le seul élément animé en continu de l'app.
 *
 * Deux anneaux se dilatent en décalé quand Raoul écoute : le mouvement dit
 * « le micro est ouvert » sans texte, et s'arrête net dès qu'il ne l'est plus.
 * Il respecte la préférence système de réduction des animations — une pulsation
 * permanente est exactement ce que ce réglage cherche à supprimer.
 */
export function Orb({ state, enabled, onPress, onLongPress }: Props) {
  const pulse = useRef(new Animated.Value(0)).current;
  const press = useRef(new Animated.Value(1)).current;
  const reduceMotion = useRef(false);

  const active = state === 'waiting' || state === 'listening' || state === 'speaking';

  useEffect(() => {
    void AccessibilityInfo.isReduceMotionEnabled().then((v) => {
      reduceMotion.current = v;
    });
  }, []);

  useEffect(() => {
    if (!active || reduceMotion.current) {
      pulse.stopAnimation();
      pulse.setValue(0);
      return;
    }
    const loop = Animated.loop(
      Animated.timing(pulse, {
        toValue: 1,
        duration: state === 'listening' ? 1600 : 2600,
        easing: Easing.out(Easing.ease),
        useNativeDriver: true,
      }),
    );
    loop.start();
    return () => loop.stop();
  }, [active, state, pulse]);

  const ring = (delay: number) => ({
    opacity: pulse.interpolate({
      inputRange: [0, delay, Math.min(delay + 0.6, 1), 1],
      outputRange: [0, 0.45, 0, 0],
    }),
    transform: [
      {
        scale: pulse.interpolate({
          inputRange: [0, delay, 1],
          outputRange: [0.85, 0.95, 1.7],
        }),
      },
    ],
  });

  const color = ACCENT[state];
  const label =
    state === 'off' ? "Activer l'écoute" : "Couper l'écoute. Appui long pour parler tout de suite.";

  return (
    <View style={styles.wrap}>
      {active && (
        <>
          <Animated.View style={[styles.ring, { borderColor: color }, ring(0)]} pointerEvents="none" />
          <Animated.View style={[styles.ring, { borderColor: color }, ring(0.35)]} pointerEvents="none" />
        </>
      )}

      <Animated.View style={{ transform: [{ scale: press }] }}>
        <Pressable
          onPress={onPress}
          onLongPress={onLongPress}
          onPressIn={() =>
            Animated.spring(press, { toValue: 0.95, useNativeDriver: true, speed: 40 }).start()
          }
          onPressOut={() =>
            Animated.spring(press, { toValue: 1, useNativeDriver: true, speed: 40 }).start()
          }
          disabled={!enabled && state === 'off'}
          accessibilityRole="button"
          accessibilityLabel={label}
          accessibilityState={{ disabled: !enabled && state === 'off' }}
          style={[
            styles.orb,
            {
              borderColor: active ? color : theme.colors.border,
              backgroundColor: active ? `${color}14` : theme.colors.surface,
            },
          ]}
        >
          {state === 'thinking' ? (
            <ActivityIndicator color={color} size="large" />
          ) : (
            <Feather name={ICON[state]} size={40} color={color} />
          )}
        </Pressable>
      </Animated.View>
    </View>
  );
}

const SIZE = 156;

const styles = StyleSheet.create({
  wrap: {
    height: SIZE + theme.space.xxl,
    alignItems: 'center',
    justifyContent: 'center',
  },
  ring: {
    position: 'absolute',
    width: SIZE,
    height: SIZE,
    borderRadius: SIZE / 2,
    borderWidth: 1.5,
  },
  orb: {
    width: SIZE,
    height: SIZE,
    borderRadius: SIZE / 2,
    borderWidth: 1.5,
    alignItems: 'center',
    justifyContent: 'center',
  },
});
