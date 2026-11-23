# ffwiz — scripting, custom tasks and internals

[Back to the main README](../README.md)

The main README covers installing ffwiz and using it. This document is the
reference for the three things that go past that: driving the engine from a
shell with `--dump`, writing your own tasks in YAML, and how the code is laid
out if you want to build on it.

## Scripting with `--dump`

`--dump` runs the same engine the UI does and prints the result, which makes the
wizard's answers reusable from a shell:

```
$ ffwiz --dump compress_video --input holiday.mp4 --output holiday_small.mp4 \
    --answers method=quality,quality=balanced,container=mp4
task     compress_video — Compress a video
input    holiday.mp4  (2s, video h264 320x240, audio aac)
output   holiday_small.mp4

steps
  method         answered choice="quality"
  codec          default  choice="h264"
  quality        answered choice="balanced"
  audio          default  choice="copy"
  container      answered choice="mp4"

skipped
  size           does not apply: only applies when method is "size"

command
ffmpeg -n -i holiday.mp4 -c:v libx264 -crf 26 -c:a copy holiday_small.mp4
```

The duration, resolution and codec in the input line are whatever your file has;
everything else is exactly what ffwiz prints.

The text form says, for every step, whether the answer was given, defaulted or
still missing, and lists the options that were hidden and why. `--json` gives the
same thing for a program.

The printed command is not the whole of what runs: the wizard adds `-nostdin`,
`-progress pipe:1` and `-nostats` when it executes, and leaves them out of the
display, because they mean nothing in a command you paste into a shell.

## Writing your own tasks

Every task is YAML. Nothing in the wizard is per-task code, so a new task is a
new file, not a new build.

Drop `.yaml` files into `~/.config/ffwiz/` (or `$XDG_CONFIG_HOME/ffwiz`, or
`--config-dir DIR`). Each file holds a list of tasks, exactly the way the
built-in ones do. User files are merged over the built-ins **by task id**, and
steps within a task are merged **by step id**, so changing one hint does not mean
copying the whole task:

```yaml
# ~/.config/ffwiz/overrides.yaml
- id: gif                       # patch the built-in gif task
  steps:
    - id: fps
      default: "15"             # ...and only this field of it

- id: resize
  disabled: true                # take a built-in task out of the list
```

### The shipped tasks

| id | label |
| --- | --- |
| `change_speed` | Change the speed |
| `compress_video` | Compress a video |
| `concat` | Join clips into one file |
| `convert_video` | Convert video to another format |
| `extract_audio` | Extract the audio |
| `extract_frames` | Extract frames or a thumbnail |
| `gif` | Make a GIF |
| `resize` | Resize a video |
| `strip_metadata` | Strip metadata |
| `trim` | Trim or cut a clip |
| `burn_subtitles` | Burn in subtitles (greyed out with the reason if your ffmpeg has no libass) |

`convert_video` also ships presets — *Web-optimised MP4*, *WhatsApp-friendly
MP4*, *YouTube upload* — and `compress_video` ships *Discord under 25 MB* and
*Smallest file (H.265)*. A preset is just a named set of answers, offered before
the questions; you can then change any answer, and asking to answer everything
yourself is always the last row.

### Schema

A **task**:

| Field | Meaning |
| --- | --- |
| `id` | short unique name, used by `--dump` and `--answers` |
| `label` | the line the task list shows |
| `description` | the longer line under it |
| `requires` | `video`, `audio`, `subtitles`: which streams the file must have |
| `requires_filter` | an ffmpeg filter the whole task needs; without it the task is not offered |
| `steps` | the questions, in order |
| `presets` | optional named answer sets |
| `disabled` | hides the task (a user override can remove a built-in this way) |

A **step**:

| Field | Meaning |
| --- | --- |
| `id` | unique within the task; answers and conditions refer to it |
| `prompt` | the question |
| `hint` | the "why" line shown under the highlighted answer |
| `type` | `choice`, `multi`, `input`, `time_range`, `resolution`, `confirm`, `concat` |
| `choices` | the options of a `choice` or `multi` step |
| `default` | the answer used when the step is active and you press `enter` through it |
| `when` | a condition; a step whose condition is false is skipped, and any answer it held is cleared |
| `validate` | `regex`, `min`, `max` for an `input` step, and `within_input` to keep a time inside the clip |
| `template` | output arguments the answer turns into, with `{value}` substituted |
| `input_template` | the same, but emitted *before* `-i` — where a seek belongs |
| `skips` | later steps that stop mattering once this one is answered |
| `args` | output arguments a `confirm` step contributes when the answer is yes |
| `filters` | filters this step contributes to the merged `-vf`/`-af` chain |
| `target` | which chain: `video` (the default) or `audio` |
| `custom` | offer a "Custom..." entry beside a `resolution` step's presets |
| `unit`, `placeholder`, `min_label`, `max_label` | display hints |
| `time_scale` | marks a step whose answer is a speed multiplier, so the progress bar stays honest |
| `compute` | makes the engine calculate the arguments from the input (see below) |

A **choice**:

| Field | Meaning |
| --- | --- |
| `label` | what the list shows |
| `value` | what the answer is recorded as; matches `answer_is` conditions |
| `hint` | the "why" line |
| `args` | output arguments, emitted after `-i` |
| `input_args` | arguments emitted before `-i` — only for genuine input options |
| `output_args` | arguments emitted after the filters, so they can override what a filter set |
| `filters`, `audio_filters`, `target` | filters this choice contributes |
| `filter_complex`, `maps` | a whole filtergraph instead of simple filters (both are required together) |
| `output_ext` | the extension the answer implies; the first active choice that gives one wins |
| `default` | the answer this step falls back to |
| `when` | hides the choice when it does not hold |
| `requires_encoder`, `requires_muxer`, `requires_filter` | capability gates |
| `skips` | later steps this choice makes irrelevant |

A **condition** (`when`) holds when everything in it holds; combine with
`any`/`all`/`not`:

| Field | Meaning |
| --- | --- |
| `input_has` | the input has `video`, `audio` or `subtitles` |
| `min_duration` | `10s`, `1m30s` or a number of seconds |
| `platform` | the OS ffwiz is running on, as a GOOS value such as `darwin`, `linux` or `windows` |
| `answer_is`, `answer_not` | compare earlier answers, keyed by step id |
| `any`, `all`, `not` | combinators |

`compute` is how "compress to a target size" works: `kind: video_bitrate` takes
the answer as a size in MiB and works out the bitrate that lands near it for
*this* file's length, subtracting the audio bitrate and a small muxing reserve,
then fills `{bitrate}`, `{maxrate}`, `{bufsize}` in its `template`.

A **preset** is a name, a description and a map of step id to answer — nothing
else. Choosing one is exactly the same as typing those answers by hand, which is
why the steps are still walked and can still be changed.

YAML is validated when ffwiz starts: unknown step ids in `skips` or `when`, a
`filter_complex` without `maps`, a choice whose `value` is empty, two choices
with the same value, a preset answering something that is not one of that step's
choices, and the rest of the load-time rules are all errors that name the file
and the task. A bad task cannot reach the screen.

### A worked example

This is a complete task, and it works as written — drop it in
`~/.config/ffwiz/rotate.yaml` and it appears in the list next to the built-ins.

```yaml
# Turn the picture a quarter turn, or mirror it.
#
# A filter is not a stream copy: ffmpeg has to decode and re-encode the video to
# apply one, so the codec question is not asked and the container's default video
# encoder is used.
- id: rotate
  label: Rotate or flip a video
  description: Turn the picture a quarter turn, or mirror it.
  requires: { video: true }

  steps:
    - id: direction
      prompt: How should it turn?
      hint: A quarter turn swaps the width and the height.
      type: choice
      choices:
        - { label: "90° clockwise", value: cw, filters: ["transpose=1"], default: true }
        - { label: "90° anticlockwise", value: ccw, filters: ["transpose=2"] }
        - { label: "180°", value: half, filters: ["transpose=1,transpose=1"] }
        - { label: "Mirror left to right", value: mirror, filters: ["hflip"] }

    - id: audio
      prompt: Audio
      hint: Turning the picture does not touch the sound, so it is copied by default.
      type: choice
      when: { input_has: audio }
      choices:
        - { label: "Keep as is (no re-encode)", value: copy, args: ["-c:a", "copy"], default: true }
        - { label: "Re-encode to AAC", value: aac, args: ["-c:a", "aac", "-b:a", "192k"], requires_encoder: aac }

    - id: container
      prompt: Output format
      type: choice
      choices:
        - { label: MP4, value: mp4, output_ext: mp4, requires_muxer: mp4, default: true }
        - { label: MKV, value: mkv, output_ext: mkv, requires_muxer: matroska }

  presets:
    - id: quarter_turn
      label: Quarter turn, MP4
      description: Turn the picture 90° clockwise and copy the sound through untouched.
      answers: { direction: cw, audio: copy, container: mp4 }
```

Taking the defaults gives:

```
ffmpeg -n -i holiday.mp4 -vf transpose=1 -c:a copy holiday_rotate.mp4
```

and the 320x240 sample comes out 240x320, with the audio stream copied
untouched. The preset produces the same thing; `--answers direction=half`
produces `-vf transpose=1,transpose=1` instead.

A few things worth noticing, because they are the parts that bite:

- **`args` go after `-i`.** Every argument a choice contributes is an *output*
  argument, because that is what ffmpeg expects: `ffmpeg -c:v libx264 -i in.mp4
  out.mp4` treats the input as the output and fails. When you need something
  before `-i` — a fast seek, `-ss` — say so with `input_args` or
  `input_template`, and it will be placed correctly.
- **Filters from several steps are merged** into one `-vf` chain in step order,
  so `rotate` and `resize` can both contribute without either one winning.
  ffmpeg rejects a second `-vf`, which is why ffwiz never emits one.
- **Capability gates are promises.** A choice that says `requires_encoder:
  libx265` is hidden, with the reason, when the local ffmpeg has no libx265.
  ffwiz will not offer you a command that cannot run.

## Architecture

The wizard is the thin part. The ffmpeg knowledge lives in two packages that do
not import a terminal library, which is what lets the whole engine be tested
without one.

```
cmd/ffwiz/                 flags, startup checks, --dump, --list
internal/ffmpeg/           binary detection, ffprobe parsing, command building,
                           running with progress, temp dirs, path safety
internal/tasks/            the YAML schema, the loader and validator, condition
                           evaluation, filter merging, and builtin/*.yaml
internal/tasks/builtin/    the shipped tasks, embedded into the binary
internal/wizard/           Bubble Tea screens, styles, keys, the live preview
internal/format/           duration, size and bitrate formatting
internal/tempdir/          the temp-directory registry and its signal handler
```

`internal/tasks` is the heart: a task file is compiled into a `[]string` of
arguments, and the same call produces the preview, the copied command, the
executed process and the exported script, so those four can never disagree.

## Development

```
make build            # bin/ffwiz
make test             # unit and model tests; integration tests skip themselves
make test-integration # only the tests that shell out to real ffmpeg
make lint             # gofmt + go vet; golangci-lint run, if it is installed
make fmt
make run
make samples          # regenerate the tiny sample media and probe fixtures
make dump TASK=gif INPUT=clip.mp4 ARGS="--answers fps=15"
make release-check    # build every release target without publishing
```

The sample media (`av.mp4`, `part1.mp4`, `part2.mp4`, an SRT, an audio-only
file, and files with awkward names) is synthesised by `scripts/make-samples.sh`,
which documents exactly how; nothing large or copyrighted is in the repository.
The integration tests use it when it is there and skip when it is not, so
`go test ./...` passes on a machine with no ffmpeg at all.

Layout of the tests: `internal/tasks` has golden tests that pin the exact
argument list each built-in task produces from a set of answers, and a suite that
runs every task against real ffmpeg; `internal/wizard` drives whole sessions
through simulated keypresses; `internal/ffmpeg` covers probe parsing, progress
decoding, quoting and the overwrite rules.

The task catalogue above mirrors what `ffwiz --list` reports on your own machine,
which is the version that matters: it reflects the ffmpeg build you actually
have. `--list` names the tasks, and `--dump` shows what any of them would run.
