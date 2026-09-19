// Package: internal/daemon
// Feature: Agent Autopilot (community board, phase 3)
// Purpose: Relevance before drafting. Before the watcher pays for a draft it
// scores the post against what the agent knows, from four free local signals
// (each 0..1):
//
//	library     fraction of the post's keywords found in the agent's library
//	            (file names and the first 2 KB of text files, rebuilt from the
//	            chunk store every 10 minutes)
//	history     an accepted or awarded reply in the last 90 days on a post
//	            sharing at least 2 keywords (outcome ledger)
//	instruction the owner's standing instruction shares at least 1 keyword
//	routed      the post arrived through request_routed activity
//
//	score = 0.45 library + 0.25 history + 0.15 instruction + 0.15 routed
//
// divided by the sum of the weights that apply (no library index drops the
// library term, an empty instruction drops the instruction term).
//
// Keyword extraction is deliberately simple and deterministic: lowercase,
// split on non-letters, drop a fixed stop-word list and tokens shorter than 3
// characters, top 12 by frequency (ties alphabetically). Also here: the
// 24 h per-category self-tuning of the threshold from the ledger's hit rates.

package daemon

import (
	"math"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/stonkagents/agent/internal/config"
)

const (
	// autopilotMaxKeywords is how many post keywords are kept.
	autopilotMaxKeywords = 12
	// autopilotMinKeywordLen drops tokens shorter than this.
	autopilotMinKeywordLen = 3
	// autopilotLibraryIndexTTL is how often the library keyword set is rebuilt.
	autopilotLibraryIndexTTL = 10 * time.Minute
	// autopilotLibraryTextBytes is how much of a text file goes into the index.
	autopilotLibraryTextBytes = 2048
	// autopilotLibraryMaxFiles bounds one index build (names) and
	// autopilotLibraryMaxTextFiles the chunk reads for text content.
	autopilotLibraryMaxFiles     = 2000
	autopilotLibraryMaxTextFiles = 200
	// autopilotHistoryWindow is how far back the history signal looks.
	autopilotHistoryWindow = 90 * 24 * time.Hour
	// autopilotHistoryMinShared is the keyword overlap the history signal needs.
	autopilotHistoryMinShared = 2

	// Signal weights.
	autopilotWeightLibrary     = 0.45
	autopilotWeightHistory     = 0.25
	autopilotWeightInstruction = 0.15
	autopilotWeightRouted      = 0.15

	// Self-tuning (per category, every 24 h, over the last 30 days).
	autopilotTuneInterval   = 24 * time.Hour
	autopilotTuneMinSamples = 5
	autopilotTuneLowRate    = 0.15 // below: threshold + 0.2 (capped 0.9)
	autopilotTuneHighRate   = 0.50 // above: threshold - 0.1 (floored 0.1)
	autopilotTuneUpStep     = 0.2
	autopilotTuneDownStep   = 0.1
	autopilotTuneMaxUp      = 0.9
)

// autopilotStopWords is the fixed English stop-word list (about 100 words).
var autopilotStopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		the and for are but not you all any can had her was one our out has his
		how its may who did get let put say she too use with that this from they
		have been were what when your will would could should there their about
		into over then than them these those some such only other more most very
		also just like need want make made does done being where which while
		after before because here each both much many still even ever every
		since through under until again those please thanks thank hello hey
		anyone someone something anything looking help know think going come
		back take give well good best new now way who whom why yes yet own same
		off per via etc`) {
		autopilotStopWords[w] = true
	}
}

// autopilotTokens lower-cases text and splits it on non-letters, keeping the
// tokens that are neither stop words nor shorter than autopilotMinKeywordLen.
func autopilotTokens(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
	out := fields[:0]
	for _, f := range fields {
		if len([]rune(f)) < autopilotMinKeywordLen || autopilotStopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// autopilotKeywords returns the top autopilotMaxKeywords tokens of text by
// frequency, ties broken alphabetically, so the same text always yields the
// same list.
func autopilotKeywords(text string) []string {
	counts := map[string]int{}
	for _, tok := range autopilotTokens(text) {
		counts[tok]++
	}
	words := make([]string, 0, len(counts))
	for w := range counts {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool {
		if counts[words[i]] != counts[words[j]] {
			return counts[words[i]] > counts[words[j]]
		}
		return words[i] < words[j]
	})
	if len(words) > autopilotMaxKeywords {
		words = words[:autopilotMaxKeywords]
	}
	return words
}

// autopilotKeywordSet is the set of tokens of text (no cap): the shape of the
// library index and the instruction match.
func autopilotKeywordSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, tok := range autopilotTokens(text) {
		set[tok] = true
	}
	return set
}

// autopilotSharedKeywords counts the keywords of a present in set.
func autopilotSharedKeywords(a []string, set map[string]bool) int {
	n := 0
	for _, k := range a {
		if set[k] {
			n++
		}
	}
	return n
}

// autopilotRelevanceSignals is the per-signal breakdown of a score. It is the
// relevanceSignals DTO on suggestions and posted replies.
type autopilotRelevanceSignals struct {
	Library     float64 `json:"library"`
	History     float64 `json:"history"`
	Instruction float64 `json:"instruction"`
	Routed      float64 `json:"routed"`
}

// autopilotRelevanceInput is what the scorer consults.
type autopilotRelevanceInput struct {
	keywords    []string
	library     map[string]bool
	history     bool // ledger: accepted or awarded on a similar post recently
	instruction map[string]bool
	routed      bool
}

// autopilotRelevance computes the weighted score (rounded to 3 decimals) and
// its signals. Signals that cannot apply are dropped and the remaining
// weights renormalised: no library index means no library term, an empty
// owner instruction means no instruction term (history and routed always
// apply), so an agent with nothing shared and nothing written still passes
// the default threshold on a routed post (0.15 / 0.40 = 0.375) instead of
// skipping everything. With a library and an instruction the weights are
// the plain 0.45 / 0.25 / 0.15 / 0.15. Pure.
func autopilotRelevance(in autopilotRelevanceInput) (float64, autopilotRelevanceSignals) {
	var sig autopilotRelevanceSignals
	score, total := 0.0, autopilotWeightHistory+autopilotWeightRouted
	if len(in.library) > 0 {
		if n := len(in.keywords); n > 0 {
			sig.Library = float64(autopilotSharedKeywords(in.keywords, in.library)) / float64(n)
		}
		score += autopilotWeightLibrary * sig.Library
		total += autopilotWeightLibrary
	}
	if in.history {
		sig.History = 1
	}
	score += autopilotWeightHistory * sig.History
	if len(in.instruction) > 0 {
		if autopilotSharedKeywords(in.keywords, in.instruction) >= 1 {
			sig.Instruction = 1
		}
		score += autopilotWeightInstruction * sig.Instruction
		total += autopilotWeightInstruction
	}
	if in.routed {
		sig.Routed = 1
	}
	score += autopilotWeightRouted * sig.Routed
	sig.Library = autopilotRound3(sig.Library)
	return autopilotRound3(score / total), sig
}

func autopilotRound3(v float64) float64 { return math.Round(v*1000) / 1000 }

// autopilotPostKeywords extracts the keywords of a post (title and body).
func autopilotPostKeywords(p autopilotPost) []string {
	return autopilotKeywords(p.Title + "\n" + p.text())
}

// --- library index --------------------------------------------------------

// autopilotLibraryEntry is one library file as the index sees it.
type autopilotLibraryEntry struct {
	Name string
	Text string // first bytes of a text file, empty for binaries
}

// autopilotLibraryIndex is the in-memory keyword set of the agent's library,
// rebuilt at most every autopilotLibraryIndexTTL.
type autopilotLibraryIndex struct {
	mu      sync.Mutex
	words   map[string]bool
	builtAt time.Time
	files   int
	// source lists the library; nil means no library (empty set).
	source func() []autopilotLibraryEntry
}

// keywords returns the current set, rebuilding it when stale.
func (ix *autopilotLibraryIndex) keywords(now time.Time) map[string]bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.words != nil && now.Sub(ix.builtAt) < autopilotLibraryIndexTTL {
		return ix.words
	}
	words := map[string]bool{}
	files := 0
	if ix.source != nil {
		for _, e := range ix.source() {
			files++
			for tok := range autopilotKeywordSet(e.Name) {
				words[tok] = true
			}
			for tok := range autopilotKeywordSet(e.Text) {
				words[tok] = true
			}
		}
	}
	ix.words, ix.builtAt, ix.files = words, now, files
	return words
}

// invalidate forces a rebuild on the next read.
func (ix *autopilotLibraryIndex) invalidate() {
	ix.mu.Lock()
	ix.words = nil
	ix.mu.Unlock()
}

// autopilotTextExtensions are the file types whose first bytes are indexed.
var autopilotTextExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".rst": true, ".csv": true, ".tsv": true, ".json": true,
	".yaml": true, ".yml": true, ".toml": true, ".xml": true, ".html": true, ".htm": true, ".log": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".rs": true, ".java": true,
	".kt": true, ".c": true, ".h": true, ".cpp": true, ".cs": true, ".rb": true, ".php": true, ".sh": true,
	".sql": true, ".ipynb": true, ".tex": true,
}

// autopilotIsTextName reports whether name looks like a text file.
func autopilotIsTextName(name string) bool {
	return autopilotTextExtensions[strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/")))]
}

// autopilotLibrarySource lists the chunk store the daemon serves at
// /api/v1/library: every file name, plus the first autopilotLibraryTextBytes
// of the first chunk for text files (bounded).
func (s *Server) autopilotLibrarySource() []autopilotLibraryEntry {
	if s.chunkStore == nil {
		return nil
	}
	files, err := s.chunkStore.ListFiles()
	if err != nil {
		return nil
	}
	out := make([]autopilotLibraryEntry, 0, len(files))
	textReads := 0
	for i, f := range files {
		if i >= autopilotLibraryMaxFiles {
			break
		}
		e := autopilotLibraryEntry{Name: f.Filename}
		if textReads < autopilotLibraryMaxTextFiles && f.TotalChunks > 0 && autopilotIsTextName(f.Filename) {
			textReads++
			if data, err := s.chunkStore.GetChunk(f.CID, 0); err == nil {
				if len(data) > autopilotLibraryTextBytes {
					data = data[:autopilotLibraryTextBytes]
				}
				e.Text = strings.ToValidUTF8(string(data), " ")
			}
		}
		out = append(out, e)
	}
	return out
}

// --- thresholds and self-tuning ------------------------------------------

// autopilotTunedThreshold is one self-tuned per-category threshold, the
// tunedThresholds DTO value and the autopilot_state.json record.
type autopilotTunedThreshold struct {
	Threshold float64 `json:"threshold"`
	HitRate   float64 `json:"hitRate"` // 0..1
	// Posted is the posted samples the rate was computed over ("samples" on the wire).
	Posted int `json:"samples"`
	// Direction is up (threshold raised), down (lowered) or "" (kept).
	Direction string `json:"direction"`
}

// autopilotCategoryStats is a category's posted samples and hits over the
// tuning window.
type autopilotCategoryStats struct {
	Posted int
	Hits   int
}

// autopilotTuneThresholds derives the per-category thresholds from the hit
// rates: at least autopilotTuneMinSamples posted replies, hit rate below
// autopilotTuneLowRate raises the policy threshold by 0.2 (capped 0.9), above
// autopilotTuneHighRate lowers it by 0.1 (floored 0.1), anything else keeps
// it. Categories pinned in the policy are left alone (not returned). Pure.
func autopilotTuneThresholds(policy config.AutopilotConfig, stats map[string]autopilotCategoryStats) map[string]autopilotTunedThreshold {
	out := map[string]autopilotTunedThreshold{}
	for cat, st := range stats {
		if _, pinned := policy.PinnedRelevanceThreshold(cat); pinned || st.Posted < autopilotTuneMinSamples {
			continue
		}
		rate := float64(st.Hits) / float64(st.Posted)
		base := policy.RelevanceThreshold
		tuned := autopilotTunedThreshold{Threshold: base, HitRate: autopilotRound3(rate), Posted: st.Posted}
		switch {
		case rate < autopilotTuneLowRate:
			// Never below what the owner set: a policy above 0.9 stays put.
			tuned.Threshold = math.Max(base, math.Min(base+autopilotTuneUpStep, autopilotTuneMaxUp))
		case rate > autopilotTuneHighRate:
			tuned.Threshold = math.Max(base-autopilotTuneDownStep, config.AutopilotMinRelevanceThreshold)
		}
		tuned.Threshold = autopilotRound3(tuned.Threshold)
		switch {
		case tuned.Threshold > base:
			tuned.Direction = "up"
		case tuned.Threshold < base:
			tuned.Direction = "down"
		}
		out[cat] = tuned
	}
	return out
}

// autopilotEffectiveThreshold is the threshold a post in category must reach:
// the owner's pin, else the tuned value, else the policy threshold.
func autopilotEffectiveThreshold(policy config.AutopilotConfig, tuned map[string]autopilotTunedThreshold, category string) float64 {
	if v, ok := policy.PinnedRelevanceThreshold(category); ok {
		return v
	}
	if t, ok := tuned[category]; ok {
		return t.Threshold
	}
	return policy.RelevanceThreshold
}

// autopilotRichBounty reports whether the post's bounty is at least twice the
// minimum multiple: in bounty hunter mode such a post is worth a look whatever
// its relevance.
func autopilotRichBounty(policy config.AutopilotConfig, b *autopilotBounty) bool {
	return policy.Mode == config.AutopilotBounty && bountyMeetsThreshold(b, 2*policy.MinBountyMultiple)
}

// --- server side ---------------------------------------------------------

// autopilotRelevanceResult is one post's score with what produced it.
type autopilotRelevanceResult struct {
	score    float64
	signals  autopilotRelevanceSignals
	keywords []string
}

// dto returns the suggestion / outcome DTO fields (nil, nil when not scored).
func (r *autopilotRelevanceResult) dto() (*float64, *autopilotRelevanceSignals) {
	if r == nil {
		return nil, nil
	}
	score, sig := r.score, r.signals
	return &score, &sig
}

// autopilotScore scores one post from the four local signals.
func (s *Server) autopilotScore(policy config.AutopilotConfig, post autopilotPost, routed bool, now time.Time) autopilotRelevanceResult {
	st := s.autopilotSt()
	keywords := autopilotPostKeywords(post)
	score, sig := autopilotRelevance(autopilotRelevanceInput{
		keywords:    keywords,
		library:     st.library.keywords(now),
		history:     st.ledger.historyMatch(keywords, now),
		instruction: autopilotKeywordSet(policy.Instruction),
		routed:      routed,
	})
	return autopilotRelevanceResult{score: score, signals: sig, keywords: keywords}
}
