package assistant

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDebriefLive(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" || os.Getenv("LIVE") == "" {
		t.Skip("LIVE non demandé")
	}
	e := New(key, "", "").WithDeep(os.Getenv("DM"), os.Getenv("DE"))
	data := `{"conversation":"groupe PXCom- Azul technique","messages":[
{"de":"Cyril","message":"On part sur la box v2 pour DAW ou on attend le firmware ?","quand":"hier à 10h02"},
{"de":"Olivier","message":"Moi je dirais d'attendre, la v2 plante en 4G","quand":"hier à 10h15"},
{"de":"Julie","message":"Réunion client déplacée à jeudi 14h","quand":"hier à 11h00"},
{"de":"Cyril","message":"↪ en réponse à Olivier : « Moi je dirais d'attendre, la v2 plante en 4G »\nok, on attend. Mathias tu peux confirmer au client avant jeudi ?","quand":"hier à 16h30"},
{"de":"toi","de_toi":true,"message":"je regarde","quand":"hier à 17h00"}]}`
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := e.analyze(ctx, debriefPrompt(time.Now(), "Europe/Paris", "Mathias", nil, "la conversation WhatsApp « groupe PXCom- Azul technique »", ""), data)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
}
