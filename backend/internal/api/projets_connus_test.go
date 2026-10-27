package api

import (
	"context"
	"strings"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Un dépôt se donne comme on l'a sous la main ; l'atelier reçoit une forme
// qu'il sait cloner, et rien qui ressemble à une option de git.
func TestNormaliserDepot(t *testing.T) {
	ok := map[string]string{
		"mathiascoutant/cerveau":                               "mathiascoutant/cerveau",
		"https://github.com/VisionShowDev/VISIONSHOW-BACK.git": "VisionShowDev/VISIONSHOW-BACK",
		"https://github.com/foo/bar/tree/main":                 "foo/bar",
		"ssh://git@gitea.pxcom.aero:5151/px/pxsky.git":         "ssh://git@gitea.pxcom.aero:5151/px/pxsky.git",
		"git@gitea.pxcom.aero:px/ife.git":                      "git@gitea.pxcom.aero:px/ife.git",
		"https://jeton@gitea.exemple.fr/px/outil.git":          "https://gitea.exemple.fr/px/outil.git",
	}
	for in, want := range ok {
		got, err := normaliserDepot(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v ; attendu %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "--upload-pack=touch /tmp/x", "foo/bar; rm -rf", "file:///etc", "http://github.com/foo/bar", "juste-un-mot"} {
		if got, err := normaliserDepot(in); err == nil {
			t.Errorf("%q aurait dû être refusé, rendu %q", in, got)
		}
	}
	if s := slugProjet("PX Sky — Écrans"); s != "px-sky-ecrans" {
		t.Errorf("slug inattendu : %q", s)
	}
}

// Mathias donne un projet ; l'équipe y range ses fiches et lui pose ses
// questions ; ses réponses rejoignent les fiches. Toute tâche de l'atelier
// connaît ensuite le projet et clone ses dépôts.
func TestCatalogueDesProjets(t *testing.T) {
	st := runsStore(t)
	ctx := context.Background()
	reg := &registre.Registre{Projets: []registre.Projet{{
		ID: "neurorun", Nom: "Neurorun", Description: "Coach de course",
		Composants: []registre.Composant{{ID: "back", Depot: "mathiascoutant/neurorun-back"}},
	}}}
	srv := NewServer(config.Config{}, st, nil, nil).WithPerso(projets.New(reg, nil, nil))
	leo, _ := srv.equipe.Membre(equipe.IDIngenieur)
	hugo, _ := srv.equipe.Membre(equipe.IDPilote)

	p, err := srv.ajouterProjet(ctx, projetDonne{
		Nom: "PX Sky", Description: "Le back-office des contenus IFE",
		Depots: []string{"https://github.com/pxcom/pxsky.git", " "},
	})
	if err != nil || p.Slug != "px-sky" || len(p.Depots) != 1 || p.Depots[0] != "pxcom/pxsky" {
		t.Fatalf("projet mal enregistré : %+v, %v", p, err)
	}
	for _, nom := range []string{"PX Sky", "Neurorun", "Général"} {
		if _, err := srv.ajouterProjet(ctx, projetDonne{Nom: nom}); err != store.ErrProjetExiste {
			t.Errorf("%q : doublon accepté (%v)", nom, err)
		}
	}

	list, _ := srv.catalogue(ctx)
	if len(list) != 2 || list[0].ID != "neurorun" || list[0].Origine != "registre" || list[1].Etat != projetAEtudier {
		t.Fatalf("catalogue inattendu : %+v", list)
	}

	// Léo étudie, note et pose une question ; la même question ne part pas deux fois.
	if _, err := srv.appelOutilProjets(ctx, leo, "noter_fiche", `{"projet":"px-sky","sujet":"Vue d'ensemble","contenu":"API Go et front React."}`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := srv.appelOutilProjets(ctx, leo, "poser_question", `{"projet":"px-sky","question":"Quel environnement est la prod ?"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.appelOutilProjets(ctx, leo, "noter_fiche", `{"projet":"inconnu","sujet":"x","contenu":"y"}`); err == nil {
		t.Error("une fiche sur un projet inconnu a été acceptée")
	}
	if _, err := srv.appelOutilProjets(ctx, leo, "ajouter_projet", `{"nom":"Autre"}`); err == nil {
		t.Error("seul Hugo ajoute un projet")
	}
	// Une question sur un projet du registre lui crée sa fiche en base.
	if _, err := srv.appelOutilProjets(ctx, hugo, "poser_question", `{"projet":"neurorun","question":"L'app Garmin est-elle encore suivie ?"}`); err != nil {
		t.Fatal(err)
	}

	pxsky, _ := srv.projetDuCatalogue(ctx, "px-sky")
	if pxsky.Resume == nil || pxsky.Resume.Texte != "API Go et front React." || pxsky.Resume.Auteur != equipe.IDIngenieur {
		t.Errorf("la vue d'ensemble devait servir de résumé : %+v", pxsky.Resume)
	}
	if pxsky.Etat != projetQuestion || pxsky.Ouvertes != 1 || len(pxsky.Sujets) != 1 || pxsky.Questions[0].Auteur != "Léo" {
		t.Fatalf("étude mal suivie : %+v", pxsky)
	}
	if n, _ := srv.projetDuCatalogue(ctx, "neurorun"); n.Ouvertes != 1 || n.Origine != "registre" {
		t.Errorf("question du registre perdue : %+v", n)
	}

	updated, err := st.RepondreQuestion(ctx, "px-sky", pxsky.Questions[0].ID, "Celle de pxsky.aero.")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.reponsesEnFiche(ctx, updated); err != nil {
		t.Fatal(err)
	}
	pxsky, _ = srv.projetDuCatalogue(ctx, "px-sky")
	if pxsky.Etat != projetConnu || len(pxsky.Sujets) != 2 {
		t.Errorf("la réponse devait rejoindre les fiches : %+v", pxsky)
	}
	out, _ := srv.appelOutilProjets(ctx, leo, "fiches_projet", `{"projet":"px-sky","recherche":"prod"}`)
	if !strings.Contains(out, "pxsky.aero") {
		t.Errorf("la réponse de Mathias manque aux fiches : %s", out)
	}

	if d := srv.depotsConnus(ctx); len(d) != 2 || d[0] != "mathiascoutant/neurorun-back" || d[1] != "pxcom/pxsky" {
		t.Errorf("dépôts à cloner : %v", d)
	}
	if c := srv.consigneProjets(ctx); !strings.Contains(c, "PX Sky (px-sky)") || !strings.Contains(c, "noter_fiche") {
		t.Errorf("consigne des projets incomplète :\n%s", c)
	}
	if tools := srv.outilsProjets(leo); len(tools) != 4 {
		t.Errorf("Léo devait avoir 4 outils, il en a %d", len(tools))
	}
	if tools := srv.outilsProjets(hugo); len(tools) != 5 {
		t.Errorf("Hugo devait avoir 5 outils, il en a %d", len(tools))
	}
}
