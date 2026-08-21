// Package tts synthétise la voix de Raoul chez ElevenLabs.
//
// Les voix système d'iOS restent lisibles mais s'entendent : débit régulier,
// liaisons ratées, intonation plate. ElevenLabs rend une voix qui respire, au
// prix d'un aller-retour réseau — que l'on absorbe en diffusant l'audio au fil
// de sa génération plutôt qu'en attendant le fichier complet.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var ErrDisabled = errors.New("synthèse vocale non configurée (ELEVENLABS_API_KEY)")

const (
	defaultBaseURL = "https://api.elevenlabs.io/v1"

	// DefaultVoiceID : « Eric », posé et sans aspérité. Les voix françaises de
	// la Voice Library sonneraient mieux, mais l'API les refuse tant que le
	// compte est en gratuit. N'importe quel identifiant peut le remplacer via
	// ELEVENLABS_VOICE_ID.
	DefaultVoiceID = "cjVigY5qzO86Huf0OWal"

	// DefaultModel : turbo répond en ~250 ms là où multilingual_v2 demande
	// plus d'une seconde, pour une qualité très proche. Sur un assistant qu'on
	// interroge à la voix, ce délai s'entend davantage que la nuance de timbre.
	DefaultModel = "eleven_turbo_v2_5"

	// mp3 44,1 kHz / 128 kbps : le meilleur format que l'API accepte sans
	// abonnement particulier, et que le lecteur iOS ouvre nativement.
	outputFormat = "mp3_44100_128"

	// ContentType du flux renvoyé par Speak.
	ContentType = "audio/mpeg"
)

type Client struct {
	apiKey  string
	voiceID string
	model   string
	// baseURL n'est remplacé que par les tests.
	baseURL string
	http    *http.Client
}

func New(apiKey, voiceID, model string) *Client {
	if voiceID == "" {
		voiceID = DefaultVoiceID
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		apiKey:  apiKey,
		voiceID: voiceID,
		model:   model,
		baseURL: defaultBaseURL,
		// Généreux : le corps est lu en continu pendant la synthèse, le délai
		// couvre donc toute la lecture d'une réponse longue.
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c.apiKey != "" }

// VoiceID et Model servent l'écran de diagnostic de l'app.
func (c *Client) VoiceID() string { return c.voiceID }
func (c *Client) Model() string   { return c.model }

// Speak ouvre le flux audio correspondant au texte. L'appelant ferme le
// ReadCloser. Les premiers octets arrivent avant la fin de la synthèse : on
// peut les réémettre tels quels vers le téléphone, qui commence à jouer
// pendant que la suite se fabrique.
func (c *Client) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	payload, err := json.Marshal(map[string]any{
		"text":     text,
		"model_id": c.model,
		// stability basse = intonation plus variée, donc moins récitée ;
		// trop basse, la voix part en vrille sur les phrases courtes.
		"voice_settings": map[string]any{
			"stability":         0.45,
			"similarity_boost":  0.8,
			"use_speaker_boost": true,
		},
	})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/text-to-speech/%s/stream?output_format=%s", c.baseURL, c.voiceID, outputFormat)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", ContentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("synthèse vocale : %w", err)
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("synthèse vocale : statut %d : %s", resp.StatusCode, raw)
	}
	return resp.Body, nil
}
