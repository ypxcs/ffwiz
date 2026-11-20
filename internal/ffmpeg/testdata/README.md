# Test data

## What is committed

| File | Purpose |
| --- | --- |
| `av.mp4.json`, `video_only.mp4.json`, `audio_only.mp3.json` | Real `ffprobe` output, captured so probe parsing is tested against genuine ffprobe JSON on machines that have no ffmpeg installed. |

## What is generated, and gitignored

`generated/` holds the small media files and the captured ffmpeg capability listings. Neither is
committed: the media is binary noise in a diff, and the listings describe the *local* ffmpeg build,
so a stale copy would be misleading.

Regenerate both with:

```sh
make samples
```

That script:

- synthesises every media file with `ffmpeg -f lavfi`, so nothing large or copyrighted is needed
  and the whole set is well under 200 KB;
- writes `av.mp4` (H.264 + AAC), `video_only.mp4` (no audio stream), `audio_only.mp3`, `part1.mp4`
  and `part2.mp4` (for the concatenate task), `subs.srt`, and five copies of `av.mp4` under the
  awkward names the quoting tests need (`sp ace.mp4`, `quote's.mp4`, `&and.mp4`,
  `-leading-dash.mp4`, `ünïcodé.mp4`);
- captures `ffprobe ... -show_format -show_streams` JSON into `testdata/`;
- captures `ffmpeg -encoders`, `-muxers`, `-pix_fmts` and `-filters` into `generated/`.

## Why the listings matter

`TestIntegrationParseRealListings` parses the captured listings in full, and
`TestIntegrationCapabilitiesMatchRawListings` cross-checks the parsed capability set against the raw
listing text for names that are present *and* absent. The flag column is six characters wide in
`-encoders` and `-pix_fmts`, **one** character wide in `-muxers`, and one or two in `-filters`; a
parser written against a hand-made sample got that wrong, which is why these tests use real output.
