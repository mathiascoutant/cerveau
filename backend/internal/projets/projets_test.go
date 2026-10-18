package projets

import (
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

func repoWith(commits ...Commit) map[string]EtatDepot {
	return map[string]EtatDepot{"o/r": {Depot: "o/r", Branche: "main", Commits: commits}}
}

// Avec l'étiquette de révision, le verdict est sûr : on sait de quel commit
// vient l'image.
func TestSummarizeRepoParRevision(t *testing.T) {
	comp := registre.Composant{Depot: "o/r", Type: "back"}
	commits := []Commit{
		{SHA: "aaaaaaa1111", Date: "2026-10-18T10:00:00Z"},
		{SHA: "bbbbbbb2222", Date: "2026-10-17T10:00:00Z"},
	}
	r := summarizeRepo(comp, repoWith(commits...), nil, &sonde.Conteneur{Etat: "running", Revision: "aaaaaaa1111"})
	if r.Deploiement != "a_jour" {
		t.Fatalf("attendu à jour : %+v", r)
	}
	r = summarizeRepo(comp, repoWith(commits...), nil, &sonde.Conteneur{Etat: "running", Revision: "bbbbbbb2222"})
	if r.Deploiement != "en_retard" || !strings.Contains(r.Explication, "1 commit") {
		t.Fatalf("attendu en retard d'un commit : %+v", r)
	}
}

// Sans étiquette, on compare les dates, et on dit que c'est une présomption.
func TestSummarizeRepoParDate(t *testing.T) {
	comp := registre.Composant{Depot: "o/r", Type: "back"}
	commits := []Commit{{SHA: "aaaaaaa", Date: "2026-10-18T10:00:00Z"}}
	late := summarizeRepo(comp, repoWith(commits...), nil, &sonde.Conteneur{Etat: "running", DemarreLe: "2026-10-17T09:00:00Z"})
	if late.Deploiement != "en_retard" || !strings.Contains(late.Explication, "présomption") {
		t.Fatalf("attendu en retard, en présomption : %+v", late)
	}
	fresh := summarizeRepo(comp, repoWith(commits...), nil, &sonde.Conteneur{Etat: "running", DemarreLe: "2026-10-18T10:05:00Z"})
	if fresh.Deploiement != "a_jour" {
		t.Fatalf("attendu à jour : %+v", fresh)
	}
}

// Une app mobile n'a pas de conteneur : on ne prétend pas savoir si elle est
// déployée.
func TestSummarizeRepoApp(t *testing.T) {
	r := summarizeRepo(registre.Composant{Depot: "o/r", Type: "app"}, repoWith(Commit{SHA: "a", Date: "2026-10-18T10:00:00Z"}), nil, nil)
	if r.Deploiement != "inconnu" || r.DernierCommit == nil {
		t.Fatalf("app : %+v", r)
	}
}

func TestAlertes(t *testing.T) {
	e := &Etat{
		VPS: &EtatVPS{
			Systeme: sonde.Systeme{
				OS: "Ubuntu 25.04", FinSupport: "2026-01-15", HorsSupport: true,
				Disques:    []sonde.Disque{{Point: "/", Pct: 42, LibreGo: 44}},
				Memoire:    sonde.Memoire{TotalMo: 7750, DisponibleMo: 6500},
				MisesAJour: 22,
				IndexAPT:   time.Now().UTC().Format(time.RFC3339),
			},
			Conteneurs: []sonde.Conteneur{
				{Nom: "visionshow-prod-back", Ports: []sonde.Port{{IP: "0.0.0.0", Hote: 8091, Prive: 8090, Public: true}, {IP: "::", Hote: 8091, Prive: 8090, Public: true}}},
				{Nom: "cerveau-api-1", Ports: []sonde.Port{{IP: "127.0.0.1", Hote: 9087, Prive: 8080}}},
			},
		},
		Projets: []EtatProjet{{
			ID: "visionshow", Nom: "VisionShow",
			Composants: []EtatComposant{{
				Nom:   "API prod",
				HTTP:  &VerifURL{URL: "https://visionshow.fr/api/health", Statut: 200, TotalMs: 120, CertificatExpire: "x", CertificatJours: 9},
				Depot: &ResumeDepot{CI: []Execution{{Nom: "SonarQube", Statut: "completed", Conclusion: "failure"}}},
			}},
		}},
	}
	got := alerts(e)
	var texts []string
	for _, a := range got {
		texts = append(texts, a.Niveau+" : "+a.Texte)
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"critique : Ubuntu 25.04 n'est plus maintenu depuis le 15 janvier 2026", "8091 (visionshow-prod-back)", "SonarQube", "expire dans 9 jours", "22 paquet(s)"} {
		if !strings.Contains(all, want) {
			t.Errorf("alerte manquante : %q\n%s", want, all)
		}
	}
	if strings.Contains(all, "9087") {
		t.Errorf("un port publié sur 127.0.0.1 n'est pas exposé :\n%s", all)
	}
	if got[0].Niveau != "critique" {
		t.Errorf("les alertes critiques passent devant : %s", all)
	}
}

func TestComponentState(t *testing.T) {
	cases := []struct {
		c    EtatComposant
		want string
	}{
		{EtatComposant{HTTP: &VerifURL{Statut: 200, TotalMs: 100}}, "ok"},
		{EtatComposant{HTTP: &VerifURL{Statut: 502}}, "panne"},
		{EtatComposant{HTTP: &VerifURL{Statut: 200, TotalMs: 4000}}, "attention"},
		{EtatComposant{Conteneur: &sonde.Conteneur{Etat: "exited"}}, "panne"},
		{EtatComposant{Conteneur: &sonde.Conteneur{Etat: "absent"}}, "panne"},
		{EtatComposant{HTTP: &VerifURL{Statut: 200}, Depot: &ResumeDepot{Deploiement: "en_retard"}}, "attention"},
		{EtatComposant{}, "inconnu"},
	}
	for i, c := range cases {
		if got := componentState(c.c); got != c.want {
			t.Errorf("cas %d : %q, attendu %q", i, got, c.want)
		}
	}
}

func TestOwnerKey(t *testing.T) {
	g := NewGitHub("defaut", map[string]string{"VISIONSHOWDEV": "vs", "MY_ORG": "org"})
	if g.token("VisionShowDev/VISIONSHOW-BACK") != "vs" {
		t.Error("le jeton de l'organisation doit servir à ses dépôts")
	}
	if g.token("my-org/repo") != "org" {
		t.Error("un tiret dans le propriétaire se règle par un souligné")
	}
	if g.token("mathiascoutant/cerveau") != "defaut" {
		t.Error("le jeton par défaut sert aux autres")
	}
}
