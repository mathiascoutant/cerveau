package tri

import (
	"context"
	"testing"
)

// Les expéditeurs automatiques se rangent par règle fixe, sans appeler Claude
// (le trieur de ce test n'a pas de client : un appel le ferait échouer).
func TestReglesFixes(t *testing.T) {
	var tr Trieur
	ds, err := tr.Classer(context.Background(), []Mail{
		{Adresse: "pxalert@pxcom.aero", Objet: "PXCom alert"},
		{Adresse: "ProcessFlightSchedule@pxcom.aero", Objet: "Process Flight Schedule - Success"},
		{Adresse: "contact@pxcom.aero", Objet: "[PXCom Onboard Digital Services] New User Registration"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Dossier != Autres || ds[1].Dossier != ProcessFlightSchedule || ds[2].Dossier != Autres {
		t.Fatalf("règles fixes mal appliquées : %+v", ds)
	}
}

// contact@ envoie aussi de vrais messages : seule l'inscription a sa règle.
func TestRegleParObjet(t *testing.T) {
	if _, ok := Regle("contact@pxcom.aero", "Demande de devis"); ok {
		t.Fatal("un autre mail de contact@ ne doit pas être rangé d'office")
	}
}
