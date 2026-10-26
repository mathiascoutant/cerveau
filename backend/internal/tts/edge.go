package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Edge fait parler les voix neuronales de Microsoft Edge, par le service
// « voix » du compose (deploy/voix) : gratuites, humaines, sans clé ni quota.
// Cerveau HQ s'en sert quand l'app reste aux voix du téléphone.
type Edge struct {
	baseURL string
	voix    string
	http    *http.Client
}

// DefaultEdgeVoice : Rémy, la plus expressive des voix françaises de Microsoft.
const DefaultEdgeVoice = "fr-FR-RemyMultilingualNeural"

// NewEdge : baseURL vide, le service n'est pas déployé et Enabled est faux.
func NewEdge(baseURL, voix string) *Edge {
	if voix == "" {
		voix = DefaultEdgeVoice
	}
	return &Edge{
		baseURL: strings.TrimRight(baseURL, "/"),
		voix:    voix,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *Edge) Enabled() bool { return e != nil && e.baseURL != "" }

func (e *Edge) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	return e.SpeakWith(ctx, "", text)
}

// SpeakWith : voix est un nom de voix Microsoft (« fr-FR-HenriNeural ») ;
// vide, celle du client.
func (e *Edge) SpeakWith(ctx context.Context, voix, text string) (io.ReadCloser, error) {
	if !e.Enabled() {
		return nil, ErrDisabled
	}
	if voix == "" {
		voix = e.voix
	}
	payload, err := json.Marshal(map[string]string{"texte": text, "voix": voix})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/parler", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voix gratuite : %w", err)
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("voix gratuite : statut %d : %s", resp.StatusCode, raw)
	}
	return resp.Body, nil
}
