package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthorizeURL construit l'URL de consentement Slack.
//
// On ne demande que des scopes utilisateur (`user_scope`) : l'app n'a pas de
// bot, et c'est le token utilisateur qui sait ce que la personne n'a pas lu.
func AuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{
		"client_id":    {clientID},
		"user_scope":   {strings.Join(RequiredScopes, ",")},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return "https://slack.com/oauth/v2/authorize?" + q.Encode()
}

// OAuthResult est ce qu'on retient de l'échange du code.
type OAuthResult struct {
	UserToken string
	TeamName  string
	UserID    string
}

// ExchangeCode échange le code de retour contre un token utilisateur.
func ExchangeCode(ctx context.Context, clientID, clientSecret, redirectURI, code string) (OAuthResult, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"oauth.v2.access", strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return OAuthResult{}, fmt.Errorf("échange du code Slack : %w", err)
	}
	defer resp.Body.Close()

	var res struct {
		apiResponse
		Team struct {
			Name string `json:"name"`
		} `json:"team"`
		AuthedUser struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
		} `json:"authed_user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return OAuthResult{}, fmt.Errorf("réponse Slack illisible : %w", err)
	}
	if !res.OK {
		return OAuthResult{}, fmt.Errorf("slack oauth : %s", res.Error)
	}
	if res.AuthedUser.AccessToken == "" {
		return OAuthResult{}, fmt.Errorf("slack n'a pas renvoyé de token utilisateur : vérifie que les scopes sont bien déclarés en « User Token Scopes »")
	}

	return OAuthResult{
		UserToken: res.AuthedUser.AccessToken,
		TeamName:  res.Team.Name,
		UserID:    res.AuthedUser.ID,
	}, nil
}
