# ffwiz

A terminal wizard for ffmpeg.

Pick a file, answer a few plain-language questions, and watch the command build
itself in a pane at the bottom of the screen. ffwiz never hides what it is going
to run, and it only offers the options your ffmpeg build and your particular file
can actually handle. Then it runs the command with a progress bar, or hands it to
you to copy, edit or save as a script.

It is for the times you know roughly what you want ("make this smaller, MP4
please") but not the flags, and it is happy to be a teacher: every option carries
a one-line "why", and the command is visible the whole way through.

```
ffwiz                                                    Input > Task > Options > Review > Run
──────────────────────────────────────────────────────────────────────────────────────────────
1/5  Output format

> ✓ MP4  (default)
  MKV
  WebM
──────────────────────────────────────────────────────────────────────────────────────────────
The safest choice. Plays on phones, TVs and browsers.
──────────────────────────────────────────────────────────────────────────────────────────────
command
ffmpeg -n -i holiday.mp4 -c:v libx264 -crf 23 -c:a copy holiday_convert_video.mp4
──────────────────────────────────────────────────────────────────────────────────────────────
↑/k up • ↓/j down • enter select • esc/e back • q quit
```

```
ffwiz                                                    Input > Task > Options > Review > Run
──────────────────────────────────────────────────────────────────────────────────────────────
Ready to go

  Output format:     MP4
  Video codec:       H.264 (most compatible)
  Quality:           Balanced (recommended)
  Audio:             Keep as is (no re-encode)
  Optimise for streaming?:  no

  Output:            holiday_convert_video.mp4
──────────────────────────────────────────────────────────────────────────────────────────────
r runs it, c copies it, x edits it, s saves a script; esc changes an answer
──────────────────────────────────────────────────────────────────────────────────────────────
command
ffmpeg -n -i holiday.mp4 -c:v libx264 -crf 23 -c:a copy holiday_convert_video.mp4
──────────────────────────────────────────────────────────────────────────────────────────────
r run • c copy • x edit command • s save as script • esc/e back • q quit
```

## Requirements

- Go 1.26 or newer, if you build from source.
- `ffmpeg` and `ffprobe` on `PATH`. ffwiz does not bundle them and will not
  download them; if they are missing it says so and points at the usual install
  command for your system.

## Install

### Prebuilt binary

Release archives are built for Linux, macOS and Windows on amd64 and arm64, with
a checksum file alongside them. Unpack the one for your platform and put `ffwiz`
somewhere on your `PATH`.

### Go

```
go install github.com/ypxcs/ffwiz/cmd/ffwiz@latest
```

### From source

```
git clone https://github.com/ypxcs/ffwiz
cd ffwiz
make build        # writes bin/ffwiz
```

## Use

```
ffwiz                       # start at the file picker
ffwiz holiday.mp4           # skip the file picker (the summary still shows)
ffwiz --list                # print the tasks this build offers, then exit
ffwiz --dump gif --input holiday.mp4    # print the command, no UI at all
```

The flow is the breadcrumb at the top of the screen: **Input → Task → Options →
Review → Run**.

1. **Input.** Choose a file (or pass one on the command line). ffwiz probes it
   and shows what is in it: duration, size, container, codecs, resolution, audio
   tracks, subtitles.
2. **Task.** Pick what you want to do. Tasks your file cannot support are shown
   greyed out with the reason.
3. **Options.** Answer the task's questions. Each one shows a short explanation
   of the highlighted answer, and the command updates as you go. Options this
   ffmpeg build cannot produce are hidden, and the help overlay says why.
4. **Review.** Read the summary and the final command. From here you can run it,
   copy it, edit it by hand, or save it as a shell script.
5. **Run.** A progress bar with elapsed time, ETA, speed and bitrate, plus a
   togglable view of ffmpeg's own log. `q` or `ctrl+c` asks before stopping.

There are eleven tasks out of the box — convert to another format, compress to a
target size, extract the audio, trim, resize, change the speed, join clips, make
a GIF, extract frames, strip metadata, burn in subtitles — and `ffwiz --list`
prints the ones your build offers.

### Keys

| Key | What it does |
| --- | --- |
| `↑` `↓` or `k` `j` | move through a list |
| `enter` | choose the highlighted value and move on |
| `space` | tick or untick a checkbox (multi-select, yes/no) |
| `esc` or `e` | go back a step |
| `/` | filter a list to the entries containing what you type |
| `a` | on the file picker, show every file, not just media |
| `?` | the full help overlay |
| `q` or `ctrl+c` | quit; while a command is running it asks first |

On the review and run screens a few extra keys appear, and the footer always
lists whichever ones are live.

## Flags

| Flag | What it does |
| --- | --- |
| `--input FILE` | skip the file picker |
| `--config-dir DIR` | where your own task files live (default `~/.config/ffwiz`) |
| `--theme auto\|light\|dark` | pin the colour scheme instead of asking the terminal (`auto` by default) |
| `--ffmpeg PATH`, `--ffprobe PATH` | use these binaries (default `$FFWIZ_FFMPEG` / `$FFWIZ_FFPROBE`, then `PATH`) |
| `--list` | list the available tasks and exit |
| `--dump TASK` | print the command a task would produce, without starting the UI (see [Scripting with `--dump`](docs/README.md)) |
| `--answers step=value,...` | answer steps for `--dump`; repeatable, but a value may not contain a comma |
| `--answer step=value` | one answer taken verbatim, for values that do contain a comma (a time range) |
| `--output FILE` | output path for `--dump` (default: derived from the input and the task) |
| `--overwrite` | emit `-y` instead of `-n` in a `--dump`ed command |
| `--json` | print the dump as JSON |
| `--temp-dir DIR` | where `--dump` writes the files the command reads |
| `--ignore-capabilities` | do not hide anything, even if this ffmpeg cannot produce it |
| `--version` | print the ffwiz and ffmpeg versions and exit |
| `--debug` | write a log to the user cache directory (`ffwiz/ffwiz.log`) |

## Safety

ffwiz is careful about the two things a command-line tool can ruin: your files
and your shell.

- **Never a shell string.** The command is an argument list from beginning to
  end. Only the display, the clipboard and the exported script are quoted text.
  Spaces, quotes, `$`, backticks, leading dashes and unicode in file names all
  work, in every task.
- **Never a silent overwrite.** The command carries `-n`, and if the output file
  already exists ffwiz asks. Overwriting is a question whose safe answer is
  `esc`; `-y` only ever appears because you said so. It also refuses to write
  over its own input.
- **Sequence outputs are checked by hand.** With the image muxer `-n` does not
  protect existing frames, so ffwiz looks for them itself and asks in the same
  way.
- **The output folder is tested** before anything runs, and the free space is
  checked against the expected size when ffwiz can work it out.
- **Nothing is left behind.** The files a command reads (a concatenation list,
  for instance) live in a directory removed on every exit path, including
  `ctrl+c`, SIGTERM and a failed run.
- **A stopped run finalises its file.** Interrupting ffmpeg leaves a playable but
  short file rather than a truncated mess. The done screen offers to delete it.

## Colours

The palette follows the terminal's own report of its background, which is right
far more often than not. `--theme light` and `--theme dark` pin it instead, for
the terminals that do not answer (over ssh, behind some multiplexers) or answer
wrongly (a dark terminal inside a light one).

`NO_COLOR` is honoured: set it and the output carries no colour at all, while
bold and other text decoration stay, as <https://no-color.org> specifies.

## Extending ffwiz

The tasks are plain YAML, not code: a new task is a new file in `~/.config/ffwiz/`,
not a new build. Here is the shape of one — a step with a few answers, each
carrying the arguments it contributes:

```yaml
- id: rotate
  label: Rotate or flip a video
  requires: { video: true }
  steps:
    - id: direction
      prompt: How should it turn?
      type: choice
      choices:
        - { label: "90° clockwise", value: cw, filters: ["transpose=1"], default: true }
        - { label: "Mirror left to right", value: mirror, filters: ["hflip"] }
```

That is an excerpt, not a whole task. The full schema, the rules for overriding a
built-in, and a complete worked example that has been run against real media are
in **[docs/README.md](docs/README.md)**.
