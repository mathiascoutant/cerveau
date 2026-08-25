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
	"strings"
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

	// DefaultLanguage : la langue imposée au modèle.
	//
	// Sans elle, turbo v2.5 devine la langue du texte — et se trompe dès qu'une
	// phrase française porte un mot qui n'en a pas l'air : « Slack »,
	// « meeting », « airwing », un nom propre, un objet de mail en anglais.
	// Une seule de ces occurrences fait basculer toute la phrase en diction
	// anglaise. On ne devine pas ce qu'on sait : Raoul parle français.
	DefaultLanguage = "fr"

	// mp3 44,1 kHz / 128 kbps : le meilleur format que l'API accepte sans
	// abonnement particulier, et que le lecteur iOS ouvre nativement.
	outputFormat = "mp3_44100_128"

	// ContentType du flux renvoyé par Speak.
	ContentType = "audio/mpeg"
)

type Client struct {
	apiKey   string
	voiceID  string
	model    string
	language string
	// baseURL n'est remplacé que par les tests.
	baseURL string
	http    *http.Client
}

func New(apiKey, voiceID, model, language string) *Client {
	if voiceID == "" {
		voiceID = DefaultVoiceID
	}
	if model == "" {
		model = DefaultModel
	}
	if language == "" {
		language = DefaultLanguage
	}
	return &Client{
		apiKey:   apiKey,
		voiceID:  voiceID,
		model:    model,
		language: language,
		baseURL:  defaultBaseURL,
		// Généreux : le corps est lu en continu pendant la synthèse, le délai
		// couvre donc toute la lecture d'une réponse longue.
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c.apiKey != "" }

// VoiceID, Model et Language servent l'écran de diagnostic de l'app.
func (c *Client) VoiceID() string  { return c.voiceID }
func (c *Client) Model() string    { return c.model }
func (c *Client) Language() string { return c.language }

// language_code n'est accepté que par les modèles rapides. multilingual_v2 le
// refuse avec une erreur 422 : lui envoyer le champ rendrait Raoul muet pour
// avoir voulu mieux le faire parler.
func (c *Client) forcesLanguage() bool {
	return strings.Contains(c.model, "turbo_v2_5") || strings.Contains(c.model, "flash_v2_5")
}

// Speak ouvre le flux audio correspondant au texte. L'appelant ferme le
// ReadCloser. Les premiers octets arrivent avant la fin de la synthèse : on
// peut les réémettre tels quels vers le téléphone, qui commence à jouer
// pendant que la suite se fabrique.
func (c *Client) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	body := map[string]any{
		"text":     text,
		"model_id": c.model,
		// stability basse = intonation plus variée, donc moins récitée ;
		// trop basse, la voix part en vrille sur les phrases courtes.
		"voice_settings": map[string]any{
			"stability":         0.45,
			"similarity_boost":  0.8,
			"use_speaker_boost": true,
		},
	}
	if c.forcesLanguage() {
		body["language_code"] = c.language
	}
	payload, err := json.Marshal(body)
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

// Voice est une voix disponible sur le compte, telle que la commande `voices`
// l'affiche pour aider à en choisir une.
type Voice struct {
	ID       string
	Name     string
	Language string
	Accent   string
	Category string
}

// Voices liste les voix du compte.
//
// Elle existe pour une raison précise : la voix par défaut d'ElevenLabs est
// anglophone, et une voix anglophone lit le français avec un accent anglais,
// quoi qu'on fasse du reste. Aucun réglage ne rattrape ça — il faut changer de
// voix, donc savoir lesquelles sont accessibles au compte, ce que seule l'API
// peut dire.
func (c *Client) Voices(ctx context.Context) ([]Voice, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("liste des voix : statut %d : %s", resp.StatusCode, raw)
	}

	var payload struct {
		Voices []struct {
			VoiceID  string            `json:"voice_id"`
			Name     string            `json:"name"`
			Category string            `json:"category"`
			Labels   map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("liste des voix : réponse illisible : %w", err)
	}

	out := make([]Voice, 0, len(payload.Voices))
	for _, v := range payload.Voices {
		out = append(out, Voice{
			ID:       v.VoiceID,
			Name:     v.Name,
			Language: v.Labels["language"],
			Accent:   v.Labels["accent"],
			Category: v.Category,
		})
	}
	return out, nil
}

// SpeaksFrench dit si une voix a le français pour langue — la seule chose qui
// compte quand on cherche à faire disparaître l'accent.
func (v Voice) SpeaksFrench() bool {
	return strings.HasPrefix(strings.ToLower(v.Language), "fr") ||
		strings.Contains(strings.ToLower(v.Accent), "french")
}
