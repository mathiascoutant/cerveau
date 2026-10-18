package hote

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestKernelLessCompareNombreParNombre(t *testing.T) {
	if !kernelLess("6.14.0-9-generic", "6.14.0-37-generic") {
		t.Error("6.14.0-9 est plus vieux que 6.14.0-37")
	}
	if kernelLess("6.14.0-37-generic", "6.8.0-100-generic") {
		t.Error("6.14 est plus récent que 6.8")
	}
}

func TestLatestKernel(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"vmlinuz-6.14.0-34-generic", "vmlinuz-6.14.0-37-generic", "vmlinuz-6.14.0-9-generic", "vmlinuz", "vmlinuz.old"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := latestKernel(dir); got != "6.14.0-37-generic" {
		t.Fatalf("noyau le plus récent : %q", got)
	}
}

func TestParseUpgradable(t *testing.T) {
	out := `Listing... Done
bsdutils/plucky-updates 1:2.40.2-14ubuntu1.2 amd64 [upgradable from: 1:2.40.2-14ubuntu1]
libssl3t64/plucky-security 3.4.1-1ubuntu1.3 amd64 [upgradable from: 3.4.1-1ubuntu1.2]

WARNING: apt does not have a stable CLI interface.
`
	pkgs := parseUpgradable(out)
	if len(pkgs) != 2 {
		t.Fatalf("2 paquets attendus, %d : %v", len(pkgs), pkgs)
	}
	if pkgs[1] != "libssl3t64/plucky-security 3.4.1-1ubuntu1.3" {
		t.Fatalf("ligne mal lue : %q", pkgs[1])
	}
}

func TestEndOfSupport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ubuntu.csv")
	csv := "version,codename,series,created,release,eol,eol-server,eol-esm\n" +
		"24.04 LTS,Noble Numbat,noble,2023-10-26,2024-04-25,2029-05-31,2029-05-31,2034-04-25\n" +
		"25.04,Plucky Puffin,plucky,2024-10-10,2025-04-17,2026-01-15\n"
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	for codename, want := range map[string]string{"plucky": "2026-01-15", "noble": "2029-05-31", "inconnu": ""} {
		got, err := endOfSupport(path, codename)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s : %q, attendu %q", codename, got, want)
		}
	}
}

// Sans terminal, Docker multiplexe stdout et stderr par trames de huit octets
// d'en-tête : elles ne doivent jamais apparaître dans les lignes rendues.
func TestSplitLogsMultiplexe(t *testing.T) {
	frame := func(stream byte, text string) []byte {
		head := make([]byte, 8)
		head[0] = stream
		binary.BigEndian.PutUint32(head[4:], uint32(len(text)))
		return append(head, text...)
	}
	raw := append(frame(1, "2026-10-18T15:32:21Z démarrage\n"), frame(2, "2026-10-18T15:32:22Z erreur : base injoignable\n")...)
	lines := splitLogs(raw, false)
	if len(lines) != 2 || lines[1] != "2026-10-18T15:32:22Z erreur : base injoignable" {
		t.Fatalf("lignes : %q", lines)
	}
	if got := splitLogs([]byte("une\ndeux\n"), true); len(got) != 2 {
		t.Fatalf("avec terminal : %q", got)
	}
}

func TestEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# commentaire\nexport MONGO_DB=runapp\nMONGO_URI=\"mongodb+srv://u:p@x.mongodb.net/?a=b\"\nVIDE=\nAVEC_COMMENTAIRE=valeur # note\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"MONGO_DB": "runapp", "MONGO_URI": "mongodb+srv://u:p@x.mongodb.net/?a=b", "AVEC_COMMENTAIRE": "valeur"}
	for key, want := range cases {
		got, err := envValue(path, key)
		if err != nil || got != want {
			t.Errorf("%s : %q (%v), attendu %q", key, got, err, want)
		}
	}
	for _, key := range []string{"VIDE", "ABSENTE"} {
		if _, err := envValue(path, key); err == nil {
			t.Errorf("%s aurait dû être signalée absente", key)
		}
	}
}

// Une erreur du pilote peut citer l'URI : le mot de passe ne doit jamais sortir.
func TestCleanMongoErrorNeLaissePasLeSecret(t *testing.T) {
	uri := "mongodb+srv://cerveau:S3cr3t-tres-long@cluster0.abcde.mongodb.net/?appName=x"
	msg := cleanMongoError(errString("auth failed for "+uri+" (password S3cr3t-tres-long)"), uri)
	if contains(msg, "S3cr3t") {
		t.Fatalf("secret visible : %s", msg)
	}
}

func TestURIParts(t *testing.T) {
	uri := "mongodb://u:p%40ss@h1:27017,h2:27017/runapp?replicaSet=rs0"
	if hosts := uriHosts(uri); len(hosts) != 2 || hosts[0] != "h1:27017" {
		t.Fatalf("hôtes : %v", hosts)
	}
	if db := uriDatabase(uri); db != "runapp" {
		t.Fatalf("base : %q", db)
	}
	if db := uriDatabase("mongodb+srv://u:p@cluster.mongodb.net/?appName=x"); db != "" {
		t.Fatalf("pas de base dans le chemin : %q", db)
	}
	if msg := cleanMongoError(errString("bad auth p@ss"), uri); contains(msg, "p@ss") {
		t.Fatalf("mot de passe décodé visible : %s", msg)
	}
}

func TestPlanStages(t *testing.T) {
	doc := func(d bson.D) bson.Raw {
		raw, err := bson.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	plan := doc(bson.D{{Key: "queryPlan", Value: bson.D{
		{Key: "stage", Value: "FETCH"},
		{Key: "inputStage", Value: bson.D{{Key: "stage", Value: "IXSCAN"}, {Key: "indexName", Value: "email_1"}}},
	}}})
	stages, index := planStages(plan)
	if len(stages) != 2 || stages[0] != "FETCH" || stages[1] != "IXSCAN" || index != "email_1" {
		t.Fatalf("plan lu : %v %q", stages, index)
	}
	stages, index = planStages(doc(bson.D{{Key: "stage", Value: "COLLSCAN"}}))
	if len(stages) != 1 || stages[0] != "COLLSCAN" || index != "" {
		t.Fatalf("plan lu : %v %q", stages, index)
	}
	// MongoDB 8 : une seule étape pour une recherche simple sur un index.
	stages, index = planStages(doc(bson.D{{Key: "stage", Value: "EXPRESS_IXSCAN"}, {Key: "indexName", Value: "email_1"}}))
	if len(stages) != 1 || index != "email_1" {
		t.Fatalf("plan express lu : %v %q", stages, index)
	}
	if rawInt(doc(bson.D{{Key: "n", Value: int32(7)}}).Lookup("n")) != 7 || rawInt(doc(bson.D{{Key: "n", Value: 3.0}}).Lookup("n")) != 3 {
		t.Fatal("compteurs mal lus")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
