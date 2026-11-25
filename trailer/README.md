# Fennec trailer

A ~75 second trailer for Fennec, made entirely from code:

- **Picture**: canvas scenes in [`web/trailer.js`](web/trailer.js). Every frame
  is a pure function of time; nothing is hand-animated or imported.
- **Music and sound effects**: synthesized in Go
  ([`cmd/trailer/synth.go`](cmd/trailer/synth.go),
  [`cmd/trailer/score.go`](cmd/trailer/score.go)): felt piano, pad, bass,
  bells, keystrokes, lid thuds, whooshes and a Freeverb-style reverb.
- **Voice-over**: recorded with ElevenLabs from the lines in
  [`timeline.json`](timeline.json) and stored in `assets/vo/`.

[`timeline.json`](timeline.json) is the single source of truth. It holds the
scenes, the voice-over lines and named cues (lid closes, logo, typing,
approval click and so on). Scene lengths stretch to fit the recorded voice,
and both the canvas and the sound effects read the same resolved cue times,
so picture, music and voice stay in sync after a line is re-recorded.

## Story

| Scene | Voice-over |
| --- | --- |
| lid | Your coding agent is halfway through the job. Then you close your laptop, and everything stops. |
| meet | Meet Fennec. |
| cloud | Fennec gives your agent its own computer in the cloud, connected to your Mac. |
| brain | The thinking happens in the cloud. The hands stay on your Mac, reaching only the files and tools you approve. |
| terminal | Nothing about how you work has to change. Open your terminal, and launch Claude Code, Codex, or the agent you already use. |
| detach | Press Control-C to step away. Your session keeps working in the cloud. |
| away | Close the lid. Go for a walk. Check in from your phone. |
| return | When you're back, one command picks up right where you left off. |
| end | Fennec. Keep your agent. Leave the laptop. |

## Requirements

Go 1.24+, ffmpeg and Google Chrome (or Chromium; set `CHROME=/path` if it
isn't found). Chrome is driven headless over `--remote-debugging-pipe`, so
the tool has no third-party Go dependencies.

## Commands

Run from this directory:

```sh
# Record or update the voice-over. Only changed lines are re-recorded.
ELEVENLABS_API_KEY=... go run ./cmd/trailer vo
go run ./cmd/trailer vo -force l03      # re-record one line

go run ./cmd/trailer timeline           # resolved scene, line and cue times
go run ./cmd/trailer still 7.5 30       # PNG stills (seconds) into out/stills
go run ./cmd/trailer audio              # soundtrack only, out/audio.wav
go run ./cmd/trailer render             # out/fennec-trailer.mp4
go run ./cmd/trailer render -captions   # with burned-in captions
```

Lines without a recording are timed from an estimate, so the trailer can be
rendered and reviewed before any voice-over exists; it is silent where the
voice would be.

The final mix is normalized to -16 LUFS. The video is 1920x1080 at 30 fps,
H.264 with AAC audio. A full render takes about 1.5 minutes on 8 cores.

To preview in a browser without rendering, run `go run ./cmd/trailer serve`
and open the printed URL. It plays in real time without sound, `?captions`
adds captions, and a click restarts it.

## Voice

The voice and its settings are under `voice` in `timeline.json` (default:
ElevenLabs "Sarah", `eleven_multilingual_v2`). Changing the voice or a line
makes `vo` re-record the affected lines. The API key is read only from the
environment and is never written to disk.

## Fonts

`web/fonts/` bundles Newsreader, Inter and JetBrains Mono under the SIL Open
Font License (license texts alongside), so renders look the same on every
machine.
