#!/usr/bin/env bash
#
# Regenerates the tiny sample media and the captured ffprobe fixtures used by
# the tests and for manual checks. Run via `make samples`.
#
# All media is synthesised with lavfi so nothing large or copyrighted ever
# enters the repository. Total output is well under 200 KB.
#
# The generated media and the captured ffmpeg listings land in
# testdata/generated/, which is gitignored. The JSON fixtures land in testdata/
# and *are* committed, so probe parsing is tested against real ffprobe output
# even on machines without ffmpeg.

set -euo pipefail

cd "$(dirname "$0")/.."

ffmpeg=${FFWIZ_FFMPEG:-ffmpeg}
ffprobe=${FFWIZ_FFPROBE:-ffprobe}

media="internal/ffmpeg/testdata/generated"
fixtures="internal/ffmpeg/testdata"

mkdir -p "$media"

echo "generating sample media in $media"

# Video + audio, H.264/AAC, 2s at 320x240.
"$ffmpeg" -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=320x240:rate=25" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -t 2 \
  -c:v libx264 -pix_fmt yuv420p -crf 34 -preset veryfast \
  -c:a aac -b:a 64k -ac 2 \
  "$media/av.mp4"

# Video with no audio stream at all: exercises "Extract audio" being hidden.
"$ffmpeg" -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=160x120:rate=10" \
  -t 1 \
  -c:v libx264 -pix_fmt yuv420p -crf 40 -preset veryfast \
  -an \
  "$media/video_only.mp4"

# Audio only: exercises the task list for inputs without video.
"$ffmpeg" -hide_banner -loglevel error -y \
  -f lavfi -i "sine=frequency=440:sample_rate=44100" \
  -t 1 \
  -c:a libmp3lame -b:a 64k \
  "$media/audio_only.mp3"

# Two more inputs for the concatenate task.
for n in 1 2; do
  "$ffmpeg" -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=160x120:rate=10" \
    -t 1 \
    -c:v libx264 -pix_fmt yuv420p -crf 40 -preset veryfast -an \
    "$media/part$n.mp4"
done

# A subtitle file for the burn-in task.
cat > "$media/subs.srt" <<'EOF'
1
00:00:00,000 --> 00:00:00,800
ffwiz burns this in

2
00:00:00,900 --> 00:00:01,800
second cue
EOF

echo "capturing capability listings in $fixtures"

# The capability parser is tested against real listings, not against a
# transcription, so it stays honest when ffmpeg's table layout changes.
for listing in encoders muxers pix_fmts filters; do
  "$ffmpeg" -hide_banner "-$listing" > "$fixtures/listing-$listing.txt"
done

# Filenames that break naive quoting (plan.md §8): spaces, a single quote, an
# ampersand, a leading dash and non-ASCII.
cp "$media/av.mp4" "$media/sp ace.mp4"
cp "$media/av.mp4" "$media/quote's.mp4"
cp "$media/av.mp4" "$media/&and.mp4"
cp "$media/av.mp4" "$media/-leading-dash.mp4"
cp "$media/av.mp4" "$media/ünïcodé.mp4"

echo "capturing ffprobe fixtures in $fixtures"

# Fixtures are captured with the path relative to the repository root so the
# committed JSON does not embed a machine-specific absolute path.
for name in av.mp4 video_only.mp4 audio_only.mp3; do
  "$ffprobe" -v error -print_format json -show_format -show_streams \
    "$media/$name" > "$fixtures/$name.json"
done

echo "done:"
ls -1sh "$media"
