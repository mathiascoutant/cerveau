package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type scripted struct {
	codes []int
	calls int
}

func (s *scripted) RoundTrip(*http.Request) (*http.Response, error) {
	code := s.codes[min(s.calls, len(s.codes)-1)]
	s.calls++
	body := `{"ok":true}`
	h := http.Header{}
	if code == http.StatusTooManyRequests {
		body = `{"ok":false,"error":"ratelimited"}`
		h.Set("Retry-After", "1")
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// Un refus pour excès d'appels est attendu puis réessayé, deux fois au plus.
func TestCallReessaieApresRatelimited(t *testing.T) {
	rt := &scripted{codes: []int{429, 200}}
	c := &Client{token: "x", http: &http.Client{Transport: rt}}
	var out apiResponse
	if err := c.call(context.Background(), "conversations.history", nil, &out); err != nil || rt.calls != 2 {
		t.Fatalf("après un refus, le second essai passe : err=%v appels=%d", err, rt.calls)
	}
	rt = &scripted{codes: []int{429}}
	c = &Client{token: "x", http: &http.Client{Transport: rt}}
	if err := c.call(context.Background(), "conversations.history", nil, &out); err == nil || rt.calls != 3 {
		t.Fatalf("trois refus : erreur attendue, err=%v appels=%d", err, rt.calls)
	}
}
