// Package whatsapp gère l'API WhatsApp Business Cloud (Meta).
//
// Limite importante de l'API officielle : elle ne donne AUCUN historique et
// aucune notion de « non lu ». On ne reçoit que les messages entrants poussés
// en temps réel sur le webhook. Le statut lu/non lu est donc tenu par Cerveau
// lui-même, à partir de ce qui arrive sur le webhook.
package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const graphBase = "https://graph.facebook.com/v21.0"

// IncomingMessage est un message entrant extrait d'un payload webhook.
type IncomingMessage struct {
	PhoneNumberID string
	MessageID     string
	From          string
	FromName      string
	Body          string
	Type          string
	Timestamp     time.Time
}

// VerifySignature valide l'en-tête X-Hub-Signature-256 envoyé par Meta.
// Sans ça, n'importe qui peut poster de faux messages sur le webhook.
func VerifySignature(appSecret string, body []byte, header string) bool {
	if appSecret == "" || header == "" {
		return false
	}
	want, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	got := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(want))
}

// ParseWebhook extrait les messages texte d'un payload de webhook Meta.
func ParseWebhook(body []byte) ([]IncomingMessage, error) {
	var payload struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
					Contacts []struct {
						WaID    string `json:"wa_id"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"contacts"`
					Messages []struct {
						ID        string `json:"id"`
						From      string `json:"from"`
						Timestamp string `json:"timestamp"`
						Type      string `json:"type"`
						Text      struct {
							Body string `json:"body"`
						} `json:"text"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	var out []IncomingMessage
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			v := change.Value
			names := map[string]string{}
			for _, ct := range v.Contacts {
				names[ct.WaID] = ct.Profile.Name
			}
			for _, m := range v.Messages {
				body := m.Text.Body
				if body == "" {
					// image, audio, document… : on note au moins qu'il y a un message
					body = "[" + m.Type + "]"
				}
				out = append(out, IncomingMessage{
					PhoneNumberID: v.Metadata.PhoneNumberID,
					MessageID:     m.ID,
					From:          m.From,
					FromName:      names[m.From],
					Body:          body,
					Type:          m.Type,
					Timestamp:     parseTimestamp(m.Timestamp),
				})
			}
		}
	}
	return out, nil
}

// TestConnection vérifie que le couple phone_number_id / access token est valide.
func TestConnection(ctx context.Context, phoneNumberID, accessToken string) (displayNumber string, err error) {
	endpoint := fmt.Sprintf("%s/%s?fields=display_phone_number,verified_name", graphBase, phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res struct {
		DisplayPhoneNumber string `json:"display_phone_number"`
		VerifiedName       string `json:"verified_name"`
		Error              struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Error.Message != "" {
		return "", fmt.Errorf("whatsapp : %s", res.Error.Message)
	}
	if res.VerifiedName != "" {
		return res.VerifiedName + " (" + res.DisplayPhoneNumber + ")", nil
	}
	return res.DisplayPhoneNumber, nil
}

// MarkRead pose la double coche bleue côté WhatsApp.
func MarkRead(ctx context.Context, phoneNumberID, accessToken, messageID string) error {
	payload, _ := json.Marshal(map[string]string{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        messageID,
	})
	endpoint := fmt.Sprintf("%s/%s/messages", graphBase, phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp mark read : statut %d", resp.StatusCode)
	}
	return nil
}

func parseTimestamp(ts string) time.Time {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.Unix(n, 0)
}
