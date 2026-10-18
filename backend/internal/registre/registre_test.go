package registre

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `{
  "vps": {"nom": "VPS OVH"},
  "domaines": ["visionshow.fr"],
  "projets": [
    {"id": "neurorun", "nom": "Neurorun", "composants": [
      {"id": "back", "nom": "API", "type": "back", "url": "https://neurorun.fr/api/health", "depot": "mathiascoutant/neurorun-back", "base": "neurorun"},
      {"id": "app", "nom": "App", "type": "app", "depot": "mathiascoutant/neurorun-app"}
    ], "bases": ["neurorun"]},
    {"id": "visionshow", "nom": "VisionShow", "composants": [
      {"id": "back", "nom": "API", "type": "back", "url": "https://visionshow.fr/api/health", "depot": "mathiascoutant/neurorun-back"}
    ]}
  ],
  "bases": [{"id": "neurorun", "nom": "Neurorun", "fichier_env": "/x/.env", "variable": "MONGODB_URI"}]
}`

func load(t *testing.T, content string) (*Registre, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projets.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Charger(path)
}

func TestCharger(t *testing.T) {
	reg, err := load(t, sample)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := reg.Base("neurorun"); !ok || b.Variable != "MONGODB_URI" {
		t.Fatalf("base : %+v", b)
	}
	// Un dépôt partagé par deux composants ne compte qu'une fois.
	if got := reg.Depots(); len(got) != 2 {
		t.Fatalf("dépôts : %v", got)
	}
}

func TestChargerRefuseUneBaseInconnue(t *testing.T) {
	bad := `{"projets": [{"id": "p", "nom": "P", "composants": [{"id": "c", "base": "absente"}]}]}`
	if _, err := load(t, bad); err == nil {
		t.Fatal("une base inconnue aurait dû être refusée")
	}
}

func TestChargerRefuseUneURLInvalide(t *testing.T) {
	bad := `{"projets": [{"id": "p", "nom": "P", "composants": [{"id": "c", "url": "ftp://x"}]}]}`
	if _, err := load(t, bad); err == nil {
		t.Fatal("une URL non HTTP aurait dû être refusée")
	}
}

// L'ingénieur n'appelle que les domaines des projets : jamais une adresse
// interne de la machine ou de l'hébergeur.
func TestHoteAutorise(t *testing.T) {
	reg, err := load(t, sample)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"visionshow.fr", "live.visionshow.fr", "neurorun.fr", "NEURORUN.FR"}
	denied := []string{"", "localhost", "127.0.0.1", "169.254.169.254", "evilvisionshow.fr", "visionshow.fr.attaquant.com", "www.neurorun.fr"}
	for _, h := range allowed {
		if !reg.HoteAutorise(h) {
			t.Errorf("%q devrait être autorisé", h)
		}
	}
	for _, h := range denied {
		if reg.HoteAutorise(h) {
			t.Errorf("%q ne devrait pas être autorisé", h)
		}
	}
}
