package equipe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func TestLireFiche(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neurorun-connexion.md")
	content := "---\nprojet: neurorun\nsujet: Connexion et authentification\n---\nLe login passe par POST /api/auth/login.\n\nBcrypt coût 10.\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := lireFiche(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Projet != "neurorun" || c.Sujet != "Connexion et authentification" || c.Equipe != EquipeInformatique {
		t.Fatalf("en-tête mal lu : %+v", c)
	}
	if !strings.HasPrefix(c.Contenu, "Le login") || !strings.HasSuffix(c.Contenu, "Bcrypt coût 10.") {
		t.Fatalf("contenu mal lu : %q", c.Contenu)
	}

	bad := filepath.Join(dir, "sans-entete.md")
	_ = os.WriteFile(bad, []byte("du texte sans en-tête"), 0o600)
	if _, err := lireFiche(bad); err == nil {
		t.Fatal("une fiche sans projet ni sujet aurait dû être refusée")
	}
}

type amorceFake struct{ got []store.Connaissance }

func (a *amorceFake) Amorcer(_ context.Context, c store.Connaissance) (bool, error) {
	a.got = append(a.got, c)
	return true, nil
}

func TestAmorcer(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.md":       "---\nprojet: general\nsujet: Vue d'ensemble\n---\nUn VPS OVH.",
		"b.md":       "---\nprojet: cerveau\nsujet: Déploiement\n---\nÀ la main.",
		"ignore.txt": "pas une fiche",
	} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	dst := &amorceFake{}
	n, err := Amorcer(context.Background(), dst, dir)
	if err != nil || n != 2 || len(dst.got) != 2 {
		t.Fatalf("amorce : %d fiches, %v", n, err)
	}
}

func TestCollegues(t *testing.T) {
	roster := Roster(Voix{})
	forRaoul := Collegues(roster, IDRaoul)
	if len(forRaoul) != 2 {
		t.Fatalf("Raoul doit pouvoir passer la parole aux deux membres : %+v", forRaoul)
	}
	forIng := Collegues(roster, IDIngenieur)
	ids := []string{}
	for _, c := range forIng {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "raoul,responsable_info" {
		t.Fatalf("l'ingénieur rend la parole à Raoul ou à la responsable : %v", ids)
	}
	// Ceux qui parlent ont une voix, Hugo compris ; la veille ne parle pas,
	// elle prévient.
	for _, m := range roster {
		if m.ID != IDRaoul && m.Veille == "" && m.Voix == "" {
			t.Errorf("%s n'a pas de voix", m.ID)
		}
	}
	if hugo := membreDe(Roster(Voix{Pilote: "voix-creee"}), IDPilote); hugo.Voix != "voix-creee" {
		t.Errorf("ELEVENLABS_VOICE_PILOTE doit remplacer la voix de Hugo : %q", hugo.Voix)
	}
	for _, src := range []string{"mail", "slack", "whatsapp"} {
		if _, ok := VeilleDe(roster, src); !ok {
			t.Errorf("pas d'agent de veille pour %s", src)
		}
	}
}

func names(tools []byte) map[string]bool {
	var list []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(tools, &list)
	out := map[string]bool{}
	for _, t := range list {
		out[t.Name] = true
	}
	return out
}

// Sans sonde, pas d'outils VPS : l'ingénieur ne doit pas raconter l'échec
// d'un outil qui ne peut pas marcher.
func TestOutilsSelonLesCapacites(t *testing.T) {
	reg := &registre.Registre{Projets: []registre.Projet{{ID: "p", Nom: "P", Composants: []registre.Composant{{ID: "c", Depot: "o/r"}}}}}
	e := New(assistant.New("cle", "", ""), projets.New(reg, nil, projets.NewGitHub("", nil)), nil, Config{})
	ing, _ := e.Membre(IDIngenieur)
	raw, _ := json.Marshal(e.toolsFor(ing, e.capacites(), false))
	got := names(raw)
	for _, absent := range []string{"journaux", "base_etat", "vps_systeme", "code_chercher", "confier_a_l_ingenieur"} {
		if got[absent] {
			t.Errorf("%s exposé à tort", absent)
		}
	}
	for _, present := range []string{"etat_projets", "verifier_url", "github_depot", "code_lire", "noter", "relire_notes", "rendre_la_parole"} {
		if !got[present] {
			t.Errorf("%s manquant", present)
		}
	}

	withProbe := New(assistant.New("cle", "", ""), projets.New(reg, &sonde.Client{}, projets.NewGitHub("jeton", nil)), nil, Config{})
	raw, _ = json.Marshal(withProbe.toolsFor(ing, withProbe.capacites(), false))
	got = names(raw)
	for _, present := range []string{"journaux", "base_etat", "base_expliquer", "vps_systeme", "code_chercher"} {
		if !got[present] {
			t.Errorf("avec sonde et jeton, %s manquant", present)
		}
	}

	resp, _ := withProbe.Membre(IDResponsable)
	raw, _ = json.Marshal(withProbe.toolsFor(resp, withProbe.capacites(), false))
	got = names(raw)
	if !got["confier_a_l_ingenieur"] || got["journaux"] || got["code_lire"] {
		t.Errorf("la responsable délègue, elle ne fouille pas : %v", got)
	}
}

func TestConsigneIngenieur(t *testing.T) {
	roster := Roster(Voix{})
	c := contexte{
		moi: membreDe(roster, IDIngenieur), roster: roster, userName: "Mathias", now: time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC), tz: "Europe/Paris",
		capacites: capacites{projets: true, sonde: true, github: true, code: true},
		fiches:    []store.Connaissance{{Projet: "neurorun", Sujet: "Vue d'ensemble", Contenu: "Coach de course.", UpdatedAt: time.Now()}},
		index:     []store.Connaissance{{Projet: "neurorun", Sujet: "Connexion et authentification"}},
	}
	prompt := consigneIngenieur(c)
	for _, want := range []string{"Tu es Léo", "Claire", "TU NE MODIFIES RIEN", "Coach de course.", "neurorun · Connexion et authentification", "journaux", "rendre_la_parole"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("consigne : %q manquant", want)
		}
	}
	// Le préfixe ne dépend pas de l'heure : c'est ce qui permet le cache.
	c2 := c
	c2.now = c.now.Add(3 * time.Hour)
	a, b := consigneIngenieur(c), consigneIngenieur(c2)
	if a[:len(a)-200] == b[:len(b)-200] && a == b {
		t.Error("l'heure doit figurer dans la consigne")
	}
	if !strings.HasPrefix(b, a[:strings.Index(a, "CONTEXTE DU MOMENT")]) {
		t.Error("tout ce qui précède le contexte du moment doit rester stable")
	}
}

// « Vois avec l'ingénieur » se délègue toujours, et ce que Léo a trouvé lui
// est attribué : Claire ne répond pas à sa place.
func TestConsigneResponsableDelegueQuandOnLeDemande(t *testing.T) {
	roster := Roster(Voix{})
	c := contexte{
		moi: membreDe(roster, IDResponsable), roster: roster, userName: "Mathias", now: time.Date(2026, 10, 18, 18, 0, 0, 0, time.UTC), tz: "Europe/Paris",
		capacites: capacites{projets: true, sonde: true, github: true, code: true},
	}
	prompt := consigneResponsable(c)
	for _, want := range []string{"VOIR AVEC L'INGÉNIEUR", "TOUJOURS la mission avec confier_a_l_ingenieur", "Léo a regardé"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("consigne de la responsable : %q manquant", want)
		}
	}
}

// Le bâtiment montre les outils réels de chaque agent, avec leur libellé, et
// rien pour Raoul : c'est l'application, pas un agent du bâtiment.
func TestOutilsExposes(t *testing.T) {
	reg := &registre.Registre{Projets: []registre.Projet{{ID: "p", Nom: "P", Composants: []registre.Composant{{ID: "c", Depot: "o/r"}}}}}
	e := New(assistant.New("cle", "", ""), projets.New(reg, &sonde.Client{}, projets.NewGitHub("jeton", nil)), nil, Config{})
	ing, _ := e.Membre(IDIngenieur)
	outils := e.Outils(ing)
	byName := map[string]string{}
	for _, o := range outils {
		byName[o.Nom] = o.Libelle
	}
	if byName["journaux"] != "Journaux" || byName["code_lire"] != "Lecture du code" {
		t.Errorf("outils de l'ingénieur : %+v", outils)
	}
	if byName["rendre_la_parole"] == "" {
		t.Error("le passage de parole fait partie de ses outils")
	}
	raoul, _ := e.Membre(IDRaoul)
	if got := e.Outils(raoul); len(got) != 0 {
		t.Errorf("Raoul n'a pas d'outils d'équipe : %+v", got)
	}
	var sonde bool
	for _, c := range e.Connecteurs() {
		if c.Nom == "Sonde du VPS" {
			sonde = c.Actif
		}
	}
	if !sonde {
		t.Error("la sonde est configurée : son connecteur doit être actif")
	}
}

func membreDe(roster []Membre, id string) Membre {
	for _, m := range roster {
		if m.ID == id {
			return m
		}
	}
	return Membre{}
}
