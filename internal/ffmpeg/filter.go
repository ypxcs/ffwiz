package ffmpeg

import "strings"

// EscapeFilterValue renders a string so that ffmpeg's filtergraph parser sees it
// as one literal value, whatever characters it contains.
//
// A filename inside a filter argument passes through two parsers, and each one
// consumes a backslash. The filtergraph level splits the graph on "," and ";",
// reads "[label]" links, and processes quotes; the filter-argument level then
// splits "key=value:key=value" on ":" and reads the value. On top of that, the
// shell consumes a third level before ffmpeg ever sees the argument, which is
// Quote's job.
//
// The rules below were measured against real files rather than derived from the
// documentation, which describes the levels but gives no worked example for a
// name that contains every special character at once. A name is wrapped in
// single quotes - inside which the filtergraph level treats ",", ";" and the
// bracket characters as literal - and outside those quotes ":" is escaped for
// the argument level and a single quote is escaped by closing the quoting,
// writing the escape, and reopening it.
//
// "=" is escaped too, and that one is not obvious: a filter argument whose first
// "key=value" pair is missing is read as a *shorthand* value, so an unescaped
// "=" in a filename makes ffmpeg read the part before it as an option name and
// fail with "Option not found". Measured with `movie`, and harmless for the
// explicit `filename=` form, which unescapes it.
//
// Verified against names containing every character class above at once, and
// individually: space, tab, quote, double quote, backtick, "&", "=", ":", ";",
// ",", "[", "]", "*", "?", "(", ")", "|", "<", ">", "#", "@", "+", "$", "~",
// "!", "%", a leading dash, leading and trailing spaces, and non-ASCII text; see
// TestEscapeFilterValueSurvivesRealFilenames.
//
// The escaping is only meaningful for a *filename* option (subtitles, movie),
// not for a filter's numeric or enumerated options.
func EscapeFilterValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			// One backslash for each of the two parsers.
			b.WriteString(`\\`)
		case ':', '=':
			// ":" separates one option from the next, and "=" separates an
			// option's name from its value.
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\'':
			// A quote cannot be escaped from inside single quotes: close the
			// quoting, write the escape where it counts, and reopen. Three
			// backslashes because this one is outside the quotes but still
			// inside the filtergraph argument, which strips one.
			b.WriteString(`'\\\''`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
