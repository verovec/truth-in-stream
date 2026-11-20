package speakername

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

const (
	participantsTool      = "record_participants"
	participantsMaxTokens = 1536
	tieBreakTool          = "record_speaker"
	tieBreakMaxTokens     = 384

	// maxParticipants bounds the list a model may return; a program with
	// more named voices than this is outside what the step resolves.
	maxParticipants = 24
	// introTurns is how many opening turns are sent whole: introductions name
	// most participants.
	introTurns = 20
	// maxTurnChars caps one turn in a prompt, and promptBudget the whole
	// transcript excerpt, so a three-hour program stays one bounded call.
	maxTurnChars       = 600
	promptBudget       = 24000
	tieBreakBudget     = 8000
	nameWindowWords    = 12
	minNameLikeWordLen = 3
)

const participantsSystemPrompt = "You list the people who speak in a recorded program, from its title and excerpts of its " +
	"diarized transcript. Each excerpt line starts with the speaker label in brackets. " +
	"Report a person only when the title or the excerpts name them, and quote the exact span that names them, " +
	"copied character for character. Never use outside knowledge: a person you recognize but who is not named in " +
	"the text must not be reported. Write each name as it is heard - a person introduced by first name only keeps " +
	"the first name. Do not report people who are merely talked about and never take part. " +
	"Record the list with the " + participantsTool + " tool."

const tieBreakSystemPrompt = "You decide which of a short list of candidate names is the speaker of a diarized transcript " +
	"label, from excerpts of that speaker's own turns. Choose a name only when a turn contains a statement only that " +
	"person could make, and quote that exact span, copied character for character. Never use outside knowledge of " +
	"who these people are. If the excerpts do not settle it, answer with an empty name. " +
	"Record the decision with the " + tieBreakTool + " tool."

// Config wires a Model. Provider selects the LLM backend; the keys are
// per-provider secrets read from the environment only; an empty ModelName lets
// the provider apply its default.
type Config struct {
	Provider       llm.ProviderName
	APIKey         string
	GeminiAPIKey   string
	DeepSeekAPIKey string
	ModelName      string
}

// Model is the generative edge of speaker naming: it lists participant names
// and breaks open ties. Its output is untrusted - callers validate it with
// Ground and ApplyTieBreak.
type Model struct {
	llm *llm.Client
}

// NewModel builds a Model on the shared forced-tool transport. Extra options
// (a test base URL) are forwarded to it.
func NewModel(cfg Config, opts ...llm.Option) (*Model, error) {
	client, err := llm.NewClient(llm.Config{
		Provider:       cfg.Provider,
		APIKey:         cfg.APIKey,
		GeminiAPIKey:   cfg.GeminiAPIKey,
		DeepSeekAPIKey: cfg.DeepSeekAPIKey,
		Model:          cfg.ModelName,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("speakername: %w", err)
	}
	return &Model{llm: client}, nil
}

type participantsResult struct {
	Participants []struct {
		Name  string `json:"name"`
		Quote string `json:"quote"`
	} `json:"participants"`
}

// Participants asks the model for the people named in the title and the
// name-bearing parts of the transcript.
func (m *Model) Participants(ctx context.Context, title string, turns []Turn) ([]Participant, error) {
	res, err := llm.Classify[participantsResult](ctx, m.llm, llm.Request{
		System:    participantsSystemPrompt,
		User:      "Title: " + title + "\n\nTranscript excerpts:\n" + nameBearingExcerpts(turns, promptBudget),
		MaxTokens: participantsMaxTokens,
		Tool: llm.Tool{
			Name:        participantsTool,
			Description: "Record the people who take part in the program.",
			Properties: map[string]any{
				"participants": map[string]any{
					"type":     "array",
					"maxItems": maxParticipants,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":  map[string]any{"type": "string", "description": "the person's name exactly as heard"},
							"quote": map[string]any{"type": "string", "description": "the exact span of the title or an excerpt that names them"},
						},
						"required": []string{"name", "quote"},
					},
				},
			},
			Required: []string{"participants"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("speakername: listing participants: %w", err)
	}
	out := make([]Participant, 0, len(res.Participants))
	for _, p := range res.Participants {
		out = append(out, Participant{Name: p.Name, Quote: p.Quote})
	}
	return out, nil
}

type tieBreakResult struct {
	Name  string `json:"name"`
	Quote string `json:"quote"`
}

// BreakTie asks the model which contender speaks under label, from that
// label's own turns. An empty name means the model could not tell.
func (m *Model) BreakTie(ctx context.Context, label string, contenders []string, turns []Turn) (name, quote string, err error) {
	res, err := llm.Classify[tieBreakResult](ctx, m.llm, llm.Request{
		System: tieBreakSystemPrompt,
		User: "Speaker label: " + label + "\nCandidate names: " + strings.Join(contenders, "; ") +
			"\n\nTurns of this speaker:\n" + labelExcerpts(turns, label, tieBreakBudget),
		MaxTokens: tieBreakMaxTokens,
		Tool: llm.Tool{
			Name:        tieBreakTool,
			Description: "Record which candidate is the speaker, or an empty name when undecided.",
			Properties: map[string]any{
				"name":  map[string]any{"type": "string", "description": "one of the candidate names, or empty when the turns do not settle it"},
				"quote": map[string]any{"type": "string", "description": "the exact span of a turn that identifies the speaker; empty when undecided"},
			},
			Required: []string{"name", "quote"},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("speakername: breaking tie for %s: %w", label, err)
	}
	return strings.TrimSpace(res.Name), res.Quote, nil
}

// nameBearingExcerpts renders the parts of the transcript likely to name
// someone, one "[label] text" line each, within budget bytes: the opening turns
// whole, then a window of words around every name-like word elsewhere. Every
// line is a verbatim span of its turn, so a quote copied from it grounds.
func nameBearingExcerpts(turns []Turn, budget int) string {
	var b strings.Builder
	for i, turn := range turns {
		var spans []string
		if i < introTurns {
			spans = []string{clip(turn.Text, maxTurnChars)}
		} else {
			spans = nameWindows(turn.Text)
		}
		for _, span := range spans {
			if b.Len()+len(span)+len(turn.Label)+4 > budget {
				return b.String()
			}
			fmt.Fprintf(&b, "[%s] %s\n", turn.Label, span)
		}
	}
	return b.String()
}

// labelExcerpts renders the turns of one label within budget bytes.
func labelExcerpts(turns []Turn, label string, budget int) string {
	var b strings.Builder
	for _, turn := range turns {
		if turn.Label != label {
			continue
		}
		span := clip(turn.Text, maxTurnChars)
		if b.Len()+len(span)+1 > budget {
			break
		}
		b.WriteString(span)
		b.WriteByte('\n')
	}
	return b.String()
}

// nameWindows returns the verbatim windows of text around its name-like words:
// capitalized words that do not merely open a sentence. Overlapping windows
// merge.
func nameWindows(text string) []string {
	tokens := tokenize(text)
	type window struct{ from, to int }
	var windows []window
	for i, tok := range tokens {
		if !tok.upper || len(tok.norm) < minNameLikeWordLen || opensSentence(text, tokens, i) {
			continue
		}
		w := window{from: max(0, i-nameWindowWords), to: min(len(tokens), i+nameWindowWords+1)}
		if n := len(windows); n > 0 && w.from <= windows[n-1].to {
			windows[n-1].to = w.to
			continue
		}
		windows = append(windows, w)
	}
	spans := make([]string, 0, len(windows))
	for _, w := range windows {
		spans = append(spans, text[tokens[w.from].start:tokens[w.to-1].end])
	}
	return spans
}

// opensSentence reports whether token i is the first word of the text or
// follows sentence-ending punctuation, where a capital proves nothing. A word
// directly followed by a comma is exempt: "Thomas, à vous" addresses someone.
func opensSentence(text string, tokens []token, i int) bool {
	if strings.HasPrefix(text[tokens[i].end:], ",") {
		return false
	}
	if i == 0 {
		return true
	}
	return strings.ContainsAny(text[tokens[i-1].end:tokens[i].start], ".?!")
}

// clip cuts s to at most limit bytes on a word boundary.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := strings.LastIndexByte(s[:limit], ' ')
	if cut <= 0 {
		return ""
	}
	return s[:cut]
}
