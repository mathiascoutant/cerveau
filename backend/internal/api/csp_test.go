package api

import (
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/tri"
)

func TestDejaImporte(t *testing.T) {
	imports := []importPxsky{
		{ID: "j2", Projet: "p-ey", Fichier: "EY_1226_PXCom-V1.xlsx", Cycle: "2026-12", Statut: "done", CreeLe: "2026-10-28T16:13:28Z", Version: 2},
		{ID: "j1", Projet: "p-ey", Fichier: "EY_1226_PXCom-V1.xlsx", Cycle: "2026-12", Statut: "error", CreeLe: "2026-10-28T16:03:47Z", Version: 1},
	}
	d := dejaImporte(imports, "p-ey", "2026-12", "EY_1226_PXCom-V1.xlsx")
	if d == nil || d.Job != "j2" || d.Version != 2 {
		t.Fatalf("le plus récent attendu : %+v", d)
	}
	if dejaImporte(imports, "p-ey", "2026-12", "EY_1226_PXCom-V2.xlsx") != nil || dejaImporte(imports, "p-ey", "2027-01", "EY_1226_PXCom-V1.xlsx") != nil {
		t.Fatal("autre version ou autre cycle : pas déjà importé")
	}
}

func TestConsigneCSP(t *testing.T) {
	c := consigneCSP([]importPxsky{
		{Projet: "6abe62a98b0c88bfc35c004d", Airline: "Etihad", Fichier: "EY_1226_PXCom-V1.xlsx", Cycle: "2026-12"},
		{Projet: "6aba81c9c0d57752aa59ff29", Airline: "Azul", Fichier: "Metadata_Azul_2612_V2.1.xlsx", Cycle: "2026-12"},
	}, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	for _, attendu := range []string{"Etihad (projet 6abe62a98b0c88bfc35c004d) : EY_1226_PXCom-V1.xlsx → 2026-12", "Azul (projet", "28/10/2026"} {
		if !strings.Contains(c, attendu) {
			t.Errorf("consigne sans %q", attendu)
		}
	}
}

func TestMailsDuTri(t *testing.T) {
	arr := []gandi.Arrivee{
		{Message: gandi.Message{Subject: "Etihad 1226 - Stellar", MessageID: "a@x"}, PiecesJointes: []string{"EY_1226.xlsx"}},
		{Message: gandi.Message{Subject: "Promo", MessageID: "b@x"}},
	}
	got := mailsDuTri(arr, []tri.Decision{{Dossier: tri.CSP}, {Dossier: tri.Autres}})
	if len(got) != 1 || got[0].MessageID != "a@x" || got[0].Pieces[0] != "EY_1226.xlsx" {
		t.Fatalf("%+v", got)
	}
}
