import React, { useEffect, useRef } from 'react';
import {
  AccessibilityInfo,
  ActivityIndicator,
  Animated,
  Easing,
  Pressable,
  StyleSheet,
  View,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import * as Haptics from 'expo-haptics';

import { Glass } from './glass';
import { alpha, theme } from '../theme';
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
 * C'est une bille de verre posée sur sa propre lumière : un halo coloré
 * dessous, la matière au milieu, deux anneaux qui s'en échappent quand le micro
 * est ouvert. Le mouvement dit « je t'écoute » sans texte, et s'arrête net dès
 * que ce n'est plus vrai.
 *
 * Il respecte la préférence système de réduction des animations — une pulsation
 * permanente est exactement ce que ce réglage cherche à supprimer. La couleur
 * ne porte jamais l'information seule : l'icône change avec l'état, et l'écran
 * affiche le libellé juste dessous.
 */
export function Orb({ state, enabled, onPress, onLongPress }: Props) {
  const pulse = useRef(new Animated.Value(0)).current;
  const breath = useRef(new Animated.Value(0)).current;
  const press = useRef(new Animated.Value(1)).current;
  const [still, setStill] = React.useState(false);

  const active = state === 'waiting' || state === 'listening' || state === 'speaking';

  useEffect(() => {
    let alive = true;
    void AccessibilityInfo.isReduceMotionEnabled().then((v) => alive && setStill(v));
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    if (!active || still) {
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
  }, [active, state, pulse, still]);

  // Le cœur respire en permanence, très lentement : c'est ce qui distingue une
  // bille de verre d'un bouton rond. Au repos l'amplitude est presque nulle.
  useEffect(() => {
    if (still) return;
    const loop = Animated.loop(
      Animated.sequence([
        Animated.timing(breath, {
          toValue: 1,
          duration: 2800,
          easing: Easing.inOut(Easing.sin),
          useNativeDriver: true,
        }),
        Animated.timing(breath, {
          toValue: 0,
          duration: 2800,
          easing: Easing.inOut(Easing.sin),
          useNativeDriver: true,
        }),
      ]),
    );
    loop.start();
    return () => loop.stop();
  }, [breath, still]);

  const ring = (delay: number) => ({
    opacity: pulse.interpolate({
      inputRange: [0, delay, Math.min(delay + 0.6, 1), 1],
      outputRange: [0, 0.4, 0, 0],
    }),
    transform: [
      {
        scale: pulse.interpolate({
          inputRange: [0, delay, 1],
          outputRange: [0.85, 0.95, 1.75],
        }),
      },
    ],
  });

  const color = ACCENT[state];
  const label =
    state === 'off' ? "Activer l'écoute" : "Couper l'écoute. Appui long pour parler tout de suite.";

  const coreScale = breath.interpolate({
    inputRange: [0, 1],
    outputRange: [1, active ? 1.06 : 1.02],
  });

  return (
    <View style={styles.wrap}>
      {/* Le halo est un fond, pas une ombre : il doit exister aussi sur
          Android, où shadowColor n'est pas coloré. */}
      <View style={[styles.halo, { backgroundColor: alpha(color, active ? 0.1 : 0.04) }]} pointerEvents="none" />

      {active && !still && (
        <>
          <Animated.View style={[styles.ring, { borderColor: color }, ring(0)]} pointerEvents="none" />
          <Animated.View style={[styles.ring, { borderColor: color }, ring(0.35)]} pointerEvents="none" />
        </>
      )}

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
          onPressIn={() =>
            Animated.spring(press, { toValue: 0.95, useNativeDriver: true, ...theme.motion.spring }).start()
          }
          onPressOut={() =>
            Animated.spring(press, { toValue: 1, useNativeDriver: true, ...theme.motion.spring }).start()
          }
          disabled={!enabled && state === 'off'}
          accessibilityRole="button"
          accessibilityLabel={label}
          accessibilityState={{ disabled: !enabled && state === 'off' }}
        >
          <Glass radius={SIZE / 2} tone={active ? color : undefined} style={styles.orb}>
            <Animated.View style={[styles.core, { transform: [{ scale: coreScale }] }]}>
              <LinearGradient
                colors={[alpha(color, active ? 0.42 : 0.14), alpha(color, 0.02)]}
                start={{ x: 0.2, y: 0 }}
                end={{ x: 0.8, y: 1 }}
                style={StyleSheet.absoluteFill}
              />
            </Animated.View>

            {state === 'thinking' ? (
              <ActivityIndicator color={color} size="large" />
            ) : (
              <Feather name={ICON[state]} size={40} color={color} />
            )}
          </Glass>
        </Pressable>
      </Animated.View>
    </View>
  );
}

const SIZE = 164;
const HALO = SIZE * 1.5;

const styles = StyleSheet.create({
  wrap: {
    height: SIZE + theme.space.xxl,
    alignItems: 'center',
    justifyContent: 'center',
  },
  halo: {
    position: 'absolute',
    width: HALO,
    height: HALO,
    borderRadius: HALO / 2,
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
    alignItems: 'center',
    justifyContent: 'center',
  },
  core: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    borderRadius: SIZE / 2,
    overflow: 'hidden',
  },
});
