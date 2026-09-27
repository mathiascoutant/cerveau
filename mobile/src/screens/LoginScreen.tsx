import React, { useEffect, useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, View } from 'react-native';

import { Button, Corners, Field, Notice, Panel, T } from '../design/ui';
import { alpha, tokens } from '../design/tokens';
import { ApiError, detectedApiUrl, getApiUrl, login, setApiUrl, signup } from '../api';

/**
 * L'écran de connexion. Il n'apparaît que si le serveur exige un compte :
 * un appareil des premières versions, connu par son identifiant, ne le voit
 * jamais.
 *
 * Se connecter ici retrouve tout ce qui est branché sur le compte — mails,
 * Slack, WhatsApp, Tuleap, agenda, mémoire — parce que ces connexions sont
 * rattachées au compte, pas au téléphone.
 */
export function LoginScreen({ onDone, reason }: { onDone: () => void; reason?: string }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [name, setName] = useState('');
  const [mode, setMode] = useState<'login' | 'signup'>('login');
  const [serverUrl, setServerUrl] = useState('');
  const [showServer, setShowServer] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void getApiUrl().then(setServerUrl);
  }, []);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (showServer && serverUrl.trim()) await setApiUrl(serverUrl.trim());
      if (mode === 'login') await login(email.trim(), password);
      else await signup(email.trim(), password, name.trim() || undefined);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        setError('Les inscriptions sont fermées sur ce serveur. Le compte se crée côté serveur (go run ./cmd/account), puis tu te connectes ici.');
      } else {
        setError((err as Error).message);
      }
    } finally {
      setBusy(false);
    }
  };

  const ready = email.trim().includes('@') && password.length >= 8;

  return (
    <KeyboardAvoidingView style={styles.flex} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
      <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
        <View style={styles.brand}>
          <View style={styles.mark}>
            <Corners color={alpha(tokens.colors.accent, 0.8)} size={8} />
            <T v="title" tone="accent">
              R
            </T>
          </View>
          <T v="display">Raoul</T>
          <T v="mono" tone="faint">
            {mode === 'login' ? 'connexion à ton compte' : 'nouveau compte'}
          </T>
        </View>

        {reason ? (
          <Notice tone="info" icon="lock">
            <T v="small" tone="muted">
              {reason}
            </T>
          </Notice>
        ) : null}

        <Panel corners>
          <Field label="Adresse" placeholder="moi@mondomaine.fr" value={email} onChangeText={setEmail} keyboardType="email-address" textContentType="username" autoComplete="email" />
          <Field
            label="Mot de passe"
            placeholder="8 caractères minimum"
            value={password}
            onChangeText={setPassword}
            secureTextEntry
            textContentType={mode === 'login' ? 'password' : 'newPassword'}
            onSubmitEditing={() => ready && void submit()}
          />
          {mode === 'signup' ? <Field label="Ton prénom" placeholder="Mathias" value={name} onChangeText={setName} autoCapitalize="words" /> : null}

          {error ? (
            <Notice tone="danger" icon="alert-triangle">
              <T v="small" tone="muted">
                {error}
              </T>
            </Notice>
          ) : null}

          <Button label={mode === 'login' ? 'Se connecter' : 'Créer le compte'} icon={mode === 'login' ? 'log-in' : 'user-plus'} loading={busy} disabled={!ready} onPress={() => void submit()} />
          <Button label={mode === 'login' ? 'Créer un compte' : 'J’ai déjà un compte'} variant="ghost" onPress={() => setMode((m) => (m === 'login' ? 'signup' : 'login'))} />
        </Panel>

        <T v="small" tone="faint" style={styles.hint}>
          Tes mails, Slack, WhatsApp, Tuleap et ton agenda sont rattachés au compte : les brancher une fois suffit, quel que soit le téléphone.
        </T>

        {showServer ? (
          <Panel>
            <Field label="Adresse du serveur" placeholder="https://cerveau.mondomaine.fr" value={serverUrl} onChangeText={setServerUrl} keyboardType="url" hint={`Détectée : ${detectedApiUrl()}`} />
          </Panel>
        ) : (
          <Button label={`Serveur : ${serverUrl || '…'}`} variant="ghost" icon="server" compact onPress={() => setShowServer(true)} />
        )}
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  content: { padding: tokens.space.xl, gap: tokens.space.xl, paddingTop: tokens.space.xxxl },
  brand: { alignItems: 'center', gap: tokens.space.sm },
  mark: {
    width: 56,
    height: 56,
    alignItems: 'center',
    justifyContent: 'center',
    borderWidth: 1,
    borderColor: alpha(tokens.colors.accent, 0.35),
    backgroundColor: alpha(tokens.colors.accent, 0.08),
    marginBottom: tokens.space.sm,
  },
  hint: { textAlign: 'center', paddingHorizontal: tokens.space.md },
});
