// Package stt transcrit de l'audio en texte.
//
// Sur iOS, l'app utilise par défaut la reconnaissance vocale native (on-device,
// gratuite, hors ligne). Ce package est le chemin de secours côté serveur :
// il parle à n'importe quel endpoint compatible OpenAI /v1/audio/transcriptions
// — api.openai.com, ou un whisper.cpp / faster-whisper auto-hébergé sur le VPS.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

var ErrDisabled = errors.New("transcription serveur non configurée (STT_BASE_URL / STT_API_KEY)")

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c.baseURL != "" && c.model != "" }

// Transcribe envoie l'audio et renvoie le texte reconnu.
func (c *Client) Transcribe(ctx context.Context, audio io.Reader, filename, language string) (string, error) {
	if !c.Enabled() {
		return "", ErrDisabled
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, audio); err != nil {
		return "", err
	}
	_ = mw.WriteField("model", c.model)
	if language != "" {
		_ = mw.WriteField("language", language)
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription : %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("transcription : statut %d : %s", resp.StatusCode, raw)
	}

	var res struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.Text, nil
}
