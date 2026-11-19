package speakername

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// debateFixture is a synthetic debate with the structure of the reference
// program: five principals holding long turns, two moderators steering in
// short turns, one marginal guest, and diarization bleed where a moderator's
// words land inside a principal's turn. Every name is fictional.
type debateFixture struct {
	turns []Turn
	names []string
	want  map[string]string
}

type fixtureSpeaker struct {
	label   string
	name    string
	address string
}

var (
	fixturePrincipals = []fixtureSpeaker{
		{label: "A", name: "Marine Durand", address: "Madame Durand"},
		{label: "B", name: "Paul Lefèvre", address: "Monsieur Lefèvre"},
		{label: "C", name: "Sophie Garnier", address: "Madame Garnier"},
		{label: "D", name: "Karim Benali", address: "Monsieur Benali"},
		{label: "E", name: "Hélène Rousseau", address: "Madame Rousseau"},
	}
	fixtureModerators = []fixtureSpeaker{
		{label: "F", name: "Claire Martin", address: "Madame Martin"},
		{label: "G", name: "Julien Moreau", address: "Monsieur Moreau"},
	}
	fixtureGuest = fixtureSpeaker{label: "H", name: "Thomas", address: "Thomas"}
)

const fixtureArgument = "Je veux dire aux Français que la situation exige des choix clairs, " +
	"que les chiffres publiés cette année le montrent et que notre projet y répond point par point."

func newDebateFixture() debateFixture {
	f := debateFixture{want: make(map[string]string)}
	var clock time.Duration
	say := func(label string, length time.Duration, text string) {
		f.turns = append(f.turns, Turn{Label: label, Start: clock, End: clock + length, Text: text})
		clock += length
	}
	for _, s := range slices.Concat(fixturePrincipals, fixtureModerators, []fixtureSpeaker{fixtureGuest}) {
		f.names = append(f.names, s.name)
		f.want[s.label] = s.name
	}

	say("F", 12*time.Second, "Bonsoir à tous et bienvenue dans ce grand débat, je suis Claire Martin.")
	say("G", 10*time.Second, "Et je suis Julien Moreau, nous allons animer cette soirée ensemble.")

	for round := range 6 {
		for i, p := range fixturePrincipals {
			moderator := fixtureModerators[(round+i)%2]
			other := fixturePrincipals[(i+1)%len(fixturePrincipals)]
			say(moderator.label, 8*time.Second, fmt.Sprintf("Nous passons à la question suivante. %s, vous avez deux minutes.", p.address))

			opening := fixtureArgument
			if round%2 == 0 {
				opening = fmt.Sprintf("%s, je vais vous répondre très précisément sur ce point qui compte pour le pays. %s", moderator.address, fixtureArgument)
			}
			say(p.label, 70*time.Second, opening)
			say(other.label, 6*time.Second, fmt.Sprintf("%s, ce que vous dites est inexact, laissez-moi vous répondre sur le fond de cette affaire.", p.address))
			// Diarization bleed: the moderator's call to order is attributed
			// to the principal who was speaking over it.
			bleed := strings.Join([]string{fixtureArgument, other.address + " s'il vous plaît, laissez terminer.", fixtureArgument}, " ")
			say(p.label, 50*time.Second, bleed)
			say(moderator.label, 5*time.Second, fmt.Sprintf("Merci %s.", p.address))
		}
		if round == 2 {
			say("F", 9*time.Second, "Nous accueillons maintenant un étudiant qui a une question. Thomas, à vous.")
			say("H", 20*time.Second, "Bonsoir, ma question porte sur le logement des jeunes dans les grandes villes.")
			say("F", 4*time.Second, "Merci Thomas.")
		}
		say(fixtureModerators[round%2].label, 6*time.Second, fmt.Sprintf("%s, c'est à vous pour la suite.", fixtureModerators[(round+1)%2].name))
	}
	return f
}
