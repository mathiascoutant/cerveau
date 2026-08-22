package gandi

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Le contenu de <script> et <style> n'est pas du texte à lire. Deux
	// expressions plutôt qu'une : RE2 n'a pas de références arrière.
	htmlScriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`)
	htmlStyleRE  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</\s*style\s*>`)
	// Balises qui séparent des blocs : elles deviennent des retours à la ligne.
	htmlBreakRE = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6])\b[^>]*>`)
	htmlTagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	entityRE    = regexp.MustCompile(`&#?[0-9a-zA-Z]+;`)
	// Lignes de citation d'une réponse : « > … » ou « Le 3 mars, X a écrit : ».
	quoteHeaderRE = regexp.MustCompile(`(?i)^(le .+ a écrit\s*:|on .+ wrote\s*:|-{2,}\s*message (transféré|d'origine|original).*)$`)
)

var namedEntities = map[string]string{
	"&nbsp;": " ", "&amp;": "&", "&lt;": "<", "&gt;": ">",
	"&quot;": `"`, "&apos;": "'", "&#39;": "'", "&eacute;": "é",
	"&egrave;": "è", "&ecirc;": "ê", "&agrave;": "à", "&ccedil;": "ç",
	"&ugrave;": "ù", "&ocirc;": "ô", "&icirc;": "î", "&euro;": "€",
	"&rsquo;": "'", "&lsquo;": "'", "&ldquo;": `"`, "&rdquo;": `"`,
	"&hellip;": "…", "&mdash;": "—", "&ndash;": "–",
}

// htmlToText réduit un mail HTML à quelque chose de prononçable.
//
// Ce n'est volontairement pas un vrai analyseur : un mail marketing est un
// empilement de tables dont aucune structure n'a de sens à l'oral. On garde le
// texte et les séparations de blocs, on jette le reste.
func htmlToText(html string) string {
	if strings.TrimSpace(html) == "" {
		return ""
	}
	s := htmlScriptRE.ReplaceAllString(html, " ")
	s = htmlStyleRE.ReplaceAllString(s, " ")
	s = htmlBreakRE.ReplaceAllString(s, "\n")
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = entityRE.ReplaceAllStringFunc(s, func(e string) string {
		if v, ok := namedEntities[strings.ToLower(e)]; ok {
			return v
		}
		return " "
	})
	return s
}

// collapse normalise les blancs sans écraser les paragraphes : les retours à la
// ligne portent le rythme de lecture.
func collapse(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '\t' || (unicode.IsSpace(r) && r != '\n') {
				return ' '
			}
			return r
		}, line))
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			// Une ligne vide sépare, deux n'apportent rien de plus.
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// stripQuotedReply coupe l'historique cité en fin de mail. Sans ça, Raoul lit
// six fois la même conversation dans un fil un peu long.
func stripQuotedReply(s string) string {
	body, _ := splitQuotedReply(s)
	return body
}

// splitQuotedReply sépare le message de l'historique qu'il cite.
//
// Les deux moitiés ne servent pas à la même chose, et c'est pour ça qu'on les
// garde séparées plutôt que d'en jeter une : le message seul est ce qu'on lit à
// voix haute, l'historique est ce qui permet de RÉPONDRE. Sans lui, on ignore
// ce qui a déjà été dit, qui répond à qui, et comment les gens s'appellent
// entre eux — et on écrit un mail hors sujet, poliment.
func splitQuotedReply(s string) (body, quoted string) {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if quoteHeaderRE.MatchString(strings.TrimSpace(line)) {
			return strings.TrimSpace(strings.Join(lines[:i], "\n")),
				strings.TrimSpace(unquote(lines[i:]))
		}
	}
	// Pas d'en-tête de citation : les lignes « > … » se trient une à une. Elles
	// peuvent être entrecoupées de blancs sans marquer la fin du message utile.
	kept := make([]string, 0, len(lines))
	cited := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			cited = append(cited, line)
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), strings.TrimSpace(unquote(cited))
}

// unquote retire les chevrons de citation. Les garder ferait lire « supérieur,
// supérieur, bonjour » à la synthèse vocale, et gêne le modèle plus qu'ils ne
// l'aident : la structure du fil se lit aux en-têtes, pas aux chevrons.
func unquote(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for strings.HasPrefix(trimmed, ">") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		}
		out = append(out, trimmed)
	}
	return collapse(strings.Join(out, "\n"))
}

// replyPrefixRE reconnaît les préfixes qu'empilent les clients mail. Deux mails
// d'un même fil ne partagent que ce qu'il en reste.
var replyPrefixRE = regexp.MustCompile(`(?i)^\s*((re|ré|rép|rep|fw|fwd|tr|transf)\s*(\[[0-9]+\])?\s*:\s*)+`)

// baseSubject réduit « Re: Fwd: Devis » à « devis », pour rapprocher les
// messages d'une même conversation. IMAP ne donne pas de fil : le sujet nu est
// le seul lien qu'on puisse établir sans en-têtes de threading fiables.
func baseSubject(s string) string {
	return normalizeQuery(replyPrefixRE.ReplaceAllString(strings.TrimSpace(s), ""))
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// normalizeQuery aplatit une demande dictée : casse, accents et ponctuation ne
// sont pas fiables quand la phrase vient de la reconnaissance vocale.
func normalizeQuery(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if folded, ok := mailAccents[r]; ok {
			return folded
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

var mailAccents = map[rune]rune{
	'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a',
	'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}
