package ffmpeg

import "testing"

func TestEscapeFilterValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// A plain name is wrapped in quotes so that the filtergraph level
			// cannot read anything in it as syntax.
			name: "a plain name is quoted",
			in:   "subs.srt",
			want: "'subs.srt'",
		},
		{
			name: "a space survives the quoting",
			in:   "my file.srt",
			want: "'my file.srt'",
		},
		{
			// The argument level splits options on ":", so a colon in the value
			// must be escaped even though the value is quoted.
			name: "a colon is escaped",
			in:   "sub:en.srt",
			want: `'sub\:en.srt'`,
		},
		{
			// An unescaped "=" makes ffmpeg read the part before it as an
			// option name, because a filter whose first pair has no "=" is read
			// as a shorthand value.
			name: "an equals sign is escaped",
			in:   "sub=en.srt",
			want: `'sub\=en.srt'`,
		},
		{
			// Both parsers consume a backslash, so one becomes two.
			name: "a backslash is doubled",
			in:   `back\slash.srt`,
			want: `'back\\slash.srt'`,
		},
		{
			// A quote cannot be escaped from inside single quotes: close, write
			// the escape, reopen.
			name: "a single quote closes and reopens the quoting",
			in:   "quote's.srt",
			want: `'quote'\\\''s.srt'`,
		},
		{
			// ",", ";" and the bracket characters are filtergraph syntax, but
			// inside single quotes they are literal, which is the whole point
			// of wrapping.
			name: "filtergraph punctuation is left alone inside the quotes",
			in:   "com,ma;semi[bracket].srt",
			want: "'com,ma;semi[bracket].srt'",
		},
		{
			name: "an empty value is an empty quoted string",
			in:   "",
			want: "''",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EscapeFilterValue(tc.in); got != tc.want {
				t.Errorf("EscapeFilterValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
