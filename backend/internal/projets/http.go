package projets

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// VerifURL : ce qu'on apprend en appelant une adresse, temps décomposés.
//
// Un « c'est lent » se loge dans une de ces étapes, et chacune a ses
// coupables : la résolution DNS, la connexion (le VPS saturé ou injoignable),
// la négociation TLS, puis l'attente du premier octet — le temps que
// l'application met à répondre, base de données comprise. Un temps total sans
// sa décomposition ne dit pas où chercher.
type VerifURL struct {
	URL            string `json:"url"`
	Statut         int    `json:"statut,omitempty"`
	Erreur         string `json:"erreur,omitempty"`
	TotalMs        int64  `json:"total_ms"`
	DNSMs          int64  `json:"dns_ms,omitempty"`
	ConnexionMs    int64  `json:"connexion_ms,omitempty"`
	TLSMs          int64  `json:"tls_ms,omitempty"`
	PremierOctetMs int64  `json:"premier_octet_ms,omitempty"`
	// Certificat : la fin de validité du certificat présenté, et les jours
	// qui restent.
	CertificatExpire string `json:"certificat_expire,omitempty"`
	CertificatJours  int    `json:"certificat_jours,omitempty"`
	Serveur          string `json:"serveur,omitempty"`
	TypeContenu      string `json:"type_contenu,omitempty"`
	Taille           int    `json:"taille,omitempty"`
	// Extrait : le début de la réponse, pour lire un message d'erreur.
	Extrait string `json:"extrait,omitempty"`
}

// Verifier appelle une adresse en GET et rend ce qui s'est passé. Les
// redirections ne sont pas suivies : une redirection inattendue est une
// information, pas un détour à masquer.
func Verifier(ctx context.Context, rawURL string) VerifURL {
	out := VerifURL{URL: rawURL}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		out.Erreur = "adresse invalide"
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	var dnsStart, connStart, tlsStart, reqStart time.Time
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:           func(httptrace.DNSDoneInfo) { out.DNSMs = since(dnsStart) },
		ConnectStart:      func(string, string) { connStart = time.Now() },
		ConnectDone:       func(string, string, error) { out.ConnexionMs = since(connStart) },
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(st tls.ConnectionState, _ error) {
			out.TLSMs = since(tlsStart)
			if len(st.PeerCertificates) > 0 {
				exp := st.PeerCertificates[0].NotAfter
				out.CertificatExpire = exp.UTC().Format(time.RFC3339)
				out.CertificatJours = int(time.Until(exp).Hours() / 24)
			}
		},
		GotFirstResponseByte: func() { out.PremierOctetMs = since(reqStart) },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, rawURL, nil)
	if err != nil {
		out.Erreur = err.Error()
		return out
	}
	req.Header.Set("User-Agent", "cerveau-surveillance/1")

	client := &http.Client{
		// Un transport neuf : sans lui, une connexion gardée d'un appel
		// précédent fausserait les temps de connexion et de TLS.
		Transport: &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 8 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	reqStart = time.Now()
	resp, err := client.Do(req)
	out.TotalMs = since(reqStart)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			out.Erreur = "pas de réponse en 12 secondes"
		default:
			out.Erreur = simplifyNetError(err)
		}
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	out.TotalMs = since(reqStart)
	out.Statut = resp.StatusCode
	out.Serveur = resp.Header.Get("Server")
	out.TypeContenu = resp.Header.Get("Content-Type")
	out.Taille = len(body)
	if loc := resp.Header.Get("Location"); loc != "" {
		out.Extrait = "redirige vers " + loc
	} else if utf8.Valid(body) {
		out.Extrait = excerpt(string(body), 300)
	}
	return out
}

// Repond dit si la vérification est un succès franc.
func (v VerifURL) Repond() bool {
	return v.Erreur == "" && v.Statut >= 200 && v.Statut < 400
}

func since(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return time.Since(t).Milliseconds()
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// simplifyNetError garde de l'erreur réseau ce qui se dit : « connexion
// refusée », « certificat invalide », plutôt que la pile Go entière.
func simplifyNetError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connexion refusée : rien n'écoute à cette adresse"
	case strings.Contains(msg, "no such host"):
		return "nom de domaine introuvable (DNS)"
	case strings.Contains(msg, "certificate"):
		return "certificat TLS refusé : " + msg
	case strings.Contains(msg, "timeout"):
		return "délai dépassé"
	}
	return fmt.Sprintf("échec de la requête : %s", msg)
}
