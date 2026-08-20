package slack

import "testing"

func TestFirstName(t *testing.T) {
	cases := map[string]string{
		"Olivier Dupont":     "Olivier",
		"Jean-Pierre Martin": "Jean-Pierre",
		"Olivier":            "Olivier",
		"olivier.dupont":     "Olivier",
		"olivier_dupont":     "Olivier",
		"mathias":            "Mathias",
		"  Anne  Sophie  ":   "Anne",
		"":                   "",
	}
	for in, want := range cases {
		if got := firstName(in); got != want {
			t.Errorf("firstName(%q) = %q, attendu %q", in, got, want)
		}
	}
}
