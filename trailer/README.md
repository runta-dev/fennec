# Fennec trailer

A ~27 second trailer for Fennec, made entirely from code: dark, terminal-native
and cut to a 120 BPM beat, with kinetic type that pops in word by word as it
is spoken.

- **Picture**: canvas scenes in [`web/trailer.js`](web/trailer.js). Every frame
  is a pure function of time; nothing is hand-animated or imported.
- **Music and sound effects**: synthesized in Go
  ([`cmd/trailer/synth.go`](cmd/trailer/synth.go),
  [`cmd/trailer/score.go`](cmd/trailer/score.go)): kick, clap, hats, sub
  bass, saw plucks, pads, impacts, risers, keystrokes and a Freeverb.
- **Voice-over**: the whole script recorded with ElevenLabs as one continuous
  take (`assets/vo/take.mp3`), with its character timestamps in
  `assets/vo/take.json`.

[`timeline.json`](timeline.json) is the single source of truth. Each scene
holds one sentence of the script and its named cues (lid shuts, drop hit,
typing, Ctrl-C and so on). The take's timestamps place every word: a scene
starts just before its first word, kinetic text pops on each word, and cues
can anchor to a word. `pauseBefore` opens a gap in the take between two
sentences (the take is split there, so it is still one read). The groove's
beat grid starts at the `gridScene`; the agent-name flashes and checkmarks
land on that grid in both the picture and the sound.

## Script

> You close your laptop. Your agent stops.
> Not anymore.
> Fennec runs your coding agent on its own cloud computer, connected to your Mac.
> Same terminal. Same agent. Claude Code, Codex, whatever you use.
> Hit Control-C. Walk away. It keeps working.
> Come back, run fen attach, and pick up right where you left off.
> Fennec. Keep your agent. Leave the laptop.

## Requirements

Go 1.24+, ffmpeg and Google Chrome (or Chromium; set `CHROME=/path` if it
isn't found). Chrome is driven headless over `--remote-debugging-pipe`, so
the tool has no third-party Go dependencies.

## Commands

Run from this directory:

```sh
# Record the voice-over (one request). Skipped if script and voice are unchanged.
ELEVENLABS_API_KEY=... go run ./cmd/trailer vo
go run ./cmd/trailer vo -force          # record a new take anyway

go run ./cmd/trailer timeline           # resolved scene and cue times
go run ./cmd/trailer still 7.5 12       # PNG stills (seconds) into out/stills
go run ./cmd/trailer audio              # soundtrack only, out/audio.wav
go run ./cmd/trailer audio -stems out/stems   # also bed.wav and voice.wav
go run ./cmd/trailer serve              # live preview in a browser (no sound)
go run ./cmd/trailer render             # out/fennec-trailer.mp4
```

Changing any sentence changes the script, so the take no longer matches and
the timeline falls back to estimated timings (and no voice) until `vo` is run
again.

The final mix is normalized to -14 LUFS. Under speech the music ducks so the
voice sits roughly 4-9 dB above the bed; check this with `audio -stems`
rather than by ear alone. The video is 1920x1080 at 30 fps, H.264 with AAC
audio. A full render takes about a minute on 8 cores.

## Voice

The voice and its settings are under `voice` in `timeline.json` (default:
ElevenLabs "Brian", `eleven_multilingual_v2`, speed 1.05). The API key is
read only from the environment and is never written to disk.

## Fonts

`web/fonts/` bundles Inter and JetBrains Mono under the SIL Open Font License
(license texts alongside), so renders look the same on every machine.
