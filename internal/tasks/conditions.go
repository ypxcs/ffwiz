package tasks

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ypxcs/ffwiz/internal/ffmpeg"
)

// EvalContext is everything a condition can be evaluated against: the probed
// input, the answers collected so far, and the platform the wizard runs on.
type EvalContext struct {
	// Info is the probed input. It is nil before a file is chosen, in which
	// case InputHas is false and MinDuration holds only for 0.
	Info *ffmpeg.MediaInfo
	// Answers holds resolved answers, keyed by step id.
	Answers map[string]string
	// Platform is the GOOS to compare a `platform` condition against.
	Platform string
}

// Eval reports whether the condition holds in the given context.
//
// All populated fields must hold (AND), which is what makes
// `when: {input_has: audio, answer_is: {...}}` read naturally. Use Any/All/Not
// for anything else.
func (c *Condition) Eval(ec EvalContext) bool {
	if c == nil {
		return true
	}

	if c.InputHas != "" && !inputHas(ec.Info, c.InputHas) {
		return false
	}

	if c.Platform != "" && c.Platform != ec.Platform {
		return false
	}

	if c.MinDuration != nil {
		if ec.Info == nil {
			return false
		}
		if ec.Info.Duration() < c.MinDuration.Std() {
			return false
		}
	}

	for stepID, want := range c.AnswerIs {
		if ec.Answers[stepID] != want {
			return false
		}
	}
	for stepID, unwanted := range c.AnswerNot {
		if ec.Answers[stepID] == unwanted {
			return false
		}
	}

	if len(c.Any) > 0 {
		matched := false
		for i := range c.Any {
			if c.Any[i].Eval(ec) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for i := range c.All {
		if !c.All[i].Eval(ec) {
			return false
		}
	}

	if c.Not != nil && c.Not.Eval(ec) {
		return false
	}

	return true
}

// Explain renders a condition in plain language, for the reason a step or a
// choice is not offered (plan.md §11: options are hidden *with an explanation*).
func (c *Condition) Explain() string {
	if c == nil {
		return ""
	}
	var parts []string

	if c.InputHas != "" {
		switch c.InputHas {
		case "audio":
			parts = append(parts, "needs an input with an audio stream")
		case "video":
			parts = append(parts, "needs an input with a video stream")
		case "subtitles", "subtitle":
			parts = append(parts, "needs an input with a subtitle stream")
		default:
			parts = append(parts, "needs an input with a "+c.InputHas+" stream")
		}
	}
	if c.MinDuration != nil {
		parts = append(parts, "needs an input at least "+c.MinDuration.String()+" long")
	}
	if c.Platform != "" {
		parts = append(parts, "only applies on "+platformName(c.Platform))
	}
	for _, stepID := range sortedKeys(c.AnswerIs) {
		parts = append(parts, fmt.Sprintf("only applies when %s is %q", stepID, c.AnswerIs[stepID]))
	}
	for _, stepID := range sortedKeys(c.AnswerNot) {
		parts = append(parts, fmt.Sprintf("does not apply when %s is %q", stepID, c.AnswerNot[stepID]))
	}
	for i := range c.All {
		if e := c.All[i].Explain(); e != "" {
			parts = append(parts, e)
		}
	}
	if len(c.Any) > 0 {
		var alts []string
		for i := range c.Any {
			if e := c.Any[i].Explain(); e != "" {
				alts = append(alts, e)
			}
		}
		if len(alts) > 0 {
			parts = append(parts, "applies when any of: "+strings.Join(alts, "; "))
		}
	}
	if c.Not != nil {
		if e := c.Not.Explain(); e != "" {
			parts = append(parts, "does not apply when: "+e)
		}
	}

	return strings.Join(parts, ", and ")
}

// sortedKeys returns a map's keys in a stable order, so explanations do not
// change between runs.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func inputHas(info *ffmpeg.MediaInfo, want string) bool {
	if info == nil {
		return false
	}
	switch want {
	case "video":
		return info.Has(ffmpeg.StreamVideo)
	case "audio":
		return info.Has(ffmpeg.StreamAudio)
	case "subtitles", "subtitle":
		return info.Has(ffmpeg.StreamSubtitle)
	default:
		return false
	}
}

// validInputHas lists the accepted input_has values, for validation messages.
var validInputHas = []string{"video", "audio", "subtitles"}

// validPlatforms are the GOOS values a `platform` condition may name. It is a
// closed list because the typo it prevents - "macos" for "darwin" - would
// otherwise hide a choice for ever with no error to explain why.
var validPlatforms = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js",
	"linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows",
}

// platformName is the human name for a GOOS value, for explanations.
func platformName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return goos
	}
}

// validate checks a condition tree for unknown references, so a typo like
// `answer_is: {vcodec: cpoy}` or `input_has: vidio` is a load-time error rather
// than a silently always-false gate.
//
// stepIDs maps step id to position; a condition may only read answers from
// steps earlier in the task, because that is all the engine knows when it
// evaluates the gate.
func (c *Condition) validate(stepIDs map[string]int, stepIndex int) []error {
	if c == nil {
		return nil
	}
	var errs []error

	if c.InputHas != "" {
		ok := false
		for _, v := range validInputHas {
			if c.InputHas == v {
				ok = true
				break
			}
		}
		if !ok {
			errs = append(errs, fmt.Errorf("input_has: unknown stream type %q (want one of %v)", c.InputHas, validInputHas))
		}
	}

	checkRef := func(kind string, answers map[string]string) {
		for ref := range answers {
			idx, exists := stepIDs[ref]
			if !exists {
				errs = append(errs, fmt.Errorf("%s: unknown step %q", kind, ref))
				continue
			}
			if idx >= stepIndex {
				errs = append(errs, fmt.Errorf("%s: refers to step %q, which is not earlier in the task; "+
					"conditions may only read answers that are already known", kind, ref))
			}
		}
	}
	checkRef("answer_is", c.AnswerIs)
	checkRef("answer_not", c.AnswerNot)

	if c.Platform != "" && !slices.Contains(validPlatforms, c.Platform) {
		errs = append(errs, fmt.Errorf("platform: unknown value %q (want one of %v)", c.Platform, validPlatforms))
	}

	for i := range c.Any {
		errs = append(errs, c.Any[i].validate(stepIDs, stepIndex)...)
	}
	for i := range c.All {
		errs = append(errs, c.All[i].validate(stepIDs, stepIndex)...)
	}
	errs = append(errs, c.Not.validate(stepIDs, stepIndex)...)

	return errs
}
