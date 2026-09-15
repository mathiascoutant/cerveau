package assistant

import (
	"strings"
	"testing"
	"time"
)

// Une mémoire vide ne doit rien écrire du tout. Une section « ce que tu sais de
// lui » suivie de rien lui apprend qu'il ne sait rien — et il le dira.
func TestMemoryBlockEmpty(t *testing.T) {
	if got := memoryBlock(nil); got != "" {
		t.Errorf("mémoire vide : bloc non vide %q", got)
	}
	blank := []FactView{{Kind: FactPerson, Subject: "Cyril", Content: "   "}}
	if got := memoryBlock(blank); got != "" {
		t.Errorf("contenu blanc : bloc non vide %q", got)
	}
}

// L'ordre des sections est celui de l'utilité, pas celui d'arrivée : on
// reconnaît un expéditeur bien plus souvent qu'on ne se rappelle une habitude.
func TestMemoryBlockGroupsAndOrders(t *testing.T) {
	block := memoryBlock([]FactView{
		{Kind: FactOther, Content: "Permis A2 passé en 2024"},
		{Kind: FactPreference, Subject: "les réunions", Content: "Jamais avant 10h"},
		{Kind: FactProject, Subject: "DAW", Content: "Déploiement des boxes pour PXCom"},
		{Kind: FactPerson, Subject: "Cyril", Content: "Technique chez Orange, on se tutoie"},
	})

	order := []string{"Les gens", "Ses dossiers", "Ses habitudes", "Divers"}
	at := -1
	for _, title := range order {
		i := strings.Index(block, title)
		if i < 0 {
			t.Fatalf("section %q absente du bloc :\n%s", title, block)
		}
		if i < at {
			t.Errorf("section %q hors d'ordre", title)
		}
		at = i
	}

	// Le sujet précède le contenu : c'est ce qui rend la ligne balayable.
	if !strings.Contains(block, "- Cyril — Technique chez Orange, on se tutoie") {
		t.Errorf("fiche avec sujet mal rendue :\n%s", block)
	}
	// Sans sujet, pas de tiret cadratin orphelin en tête de ligne.
	if !strings.Contains(block, "- Permis A2 passé en 2024") {
		t.Errorf("fait sans sujet mal rendu :\n%s", block)
	}
}

// Une catégorie inconnue ne doit pas faire disparaître la ligne : elle tombe
// dans « Divers ». Perdre une information parce que le modèle a inventé un
// tiroir serait la pire des issues — silencieuse.
func TestMemoryBlockKeepsUnknownKind(t *testing.T) {
	block := memoryBlock([]FactView{{Kind: "lubie", Subject: "Vélo", Content: "Va au bureau à vélo"}})
	if !strings.Contains(block, "Divers") || !strings.Contains(block, "Va au bureau à vélo") {
		t.Errorf("catégorie inconnue perdue :\n%s", block)
	}
}

// La mémoire doit arriver dans la consigne, et les règles d'écriture y être
// même quand elle est vide : c'est justement quand il ne sait rien qu'il a le
// plus à apprendre.
func TestSystemPromptCarriesMemory(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 8, 20, 19, 30, 0, 0, paris)

	empty := systemPrompt(now, "Europe/Paris", "Mathias", "mathias@exemple.fr", Sources{Mail: true}, nil)
	if !strings.Contains(empty, "CE QUE TU RETIENS") {
		t.Error("les règles d'écriture de la mémoire manquent quand elle est vide")
	}
	if strings.Contains(empty, "CE QUE TU SAIS DÉJÀ DE LUI") {
		t.Error("une mémoire vide ne doit pas ouvrir de section « ce que tu sais »")
	}

	filled := systemPrompt(now, "Europe/Paris", "Mathias", "mathias@exemple.fr", Sources{Mail: true},
		[]FactView{{Kind: FactPerson, Subject: "Cyril", Content: "Technique chez Orange"}})
	for _, needle := range []string{"CE QUE TU SAIS DÉJÀ DE LUI", "Cyril", "Technique chez Orange"} {
		if !strings.Contains(filled, needle) {
			t.Errorf("le prompt ne porte pas %q", needle)
		}
	}
}
