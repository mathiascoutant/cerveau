package atelier

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
)

// Un extrait réaliste du flux stream-json de Claude Code.
const flux = `{"type":"atelier","event":"pret","depots":["o/r"],"erreurs":[]}
{"type":"system","subtype":"init","model":"claude-opus-5-5"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Je regarde."},{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"forgot"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"cd x && npm test","description":"Lance les tests"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"1 failed"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"git push -u origin agent/abc-mdp"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"ok"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"  Corrigé sur la branche agent/abc-mdp.  ","total_cost_usd":0.4}
`

func TestLectureDuFlux(t *testing.T) {
	var evs []assistant.Event
	r := newReader(func(e assistant.Event) { evs = append(evs, e) })
	for _, l := range strings.Split(flux, "\n") {
		r.line([]byte(l))
	}
	r.closeOpen()
	if r.result == nil || r.result.Reply != "Corrigé sur la branche agent/abc-mdp." || r.failure != "" {
		t.Fatalf("résultat %+v, échec %q", r.result, r.failure)
	}
	var debuts []string
	var echecs int
	for _, e := range evs {
		if e.Type != "status" {
			t.Fatalf("événement inattendu : %+v", e)
		}
		if e.Ms == 0 {
			debuts = append(debuts, e.Label)
		}
		if e.Err != "" {
			echecs++
		}
	}
	want := []string{"Préparation des dépôts", "Recherche dans le code", "Tests", "Push de la branche"}
	if strings.Join(debuts, "|") != strings.Join(want, "|") {
		t.Errorf("étapes : %v", debuts)
	}
	if echecs != 1 {
		t.Errorf("le test raté doit être marqué en échec : %d", echecs)
	}
}

func TestEchecs(t *testing.T) {
	cas := map[string]string{
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":""}`:                "error_max_turns",
		`{"type":"atelier","event":"erreur","message":"configuration absente"}`:                    "configuration absente",
		`{"type":"atelier","event":"fin","code":1,"stderr":"Invalid API key · Please run /login"}`: "/login",
	}
	for l, attendu := range cas {
		r := newReader(nil)
		r.line([]byte(l))
		if r.result != nil || !strings.Contains(r.failure, attendu) {
			t.Errorf("%s → résultat %+v, échec %q", l, r.result, r.failure)
		}
	}
}

func TestLibelles(t *testing.T) {
	bash := func(cmd string) string {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		return Label("Bash", raw)
	}
	cas := map[string]string{
		bash("git -C ~/depots/x worktree add ./x -b agent/1-a origin/main"): "Création de la branche",
		bash(`git commit -m "fix"`):                                         "Commit",
		bash("go vet ./..."):                                                "Vérification du code",
		bash("ls"):                                                          "Commande",
		Label("Edit", nil):                                                  "Modification du code",
	}
	for got, want := range cas {
		if got != want {
			t.Errorf("libellé %q, attendu %q", got, want)
		}
	}
}
