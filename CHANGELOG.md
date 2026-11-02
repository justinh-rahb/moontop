# Changelog

All notable changes to Moontop are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are cut from this file: adding a `## [x.y.z] - YYYY-MM-DD` heading on
`main` tags and publishes that version (see [docs/RELEASING.md](docs/RELEASING.md)).

## [Unreleased]

### Fixed

- The sharp camera picture no longer flickers between the photo and the
  block picture underneath (seen in Ghostty): frames alternate between two
  image slots, and each is sent once from the pane's title line instead of
  again whenever a neighbouring pane changes.
- An `iframe` camera (such as HelixScreen's screen mirror) whose snapshot URL
  is the page itself is no longer offered; a snapshot that turns out to be a
  web page is reported plainly instead of a JPEG decoding error. PNG and GIF
  snapshots now work.
- A redirect from the printer to another host (or port) no longer carries
  your Moonraker API key or login token with it.

## [0.2.0] - 2026-11-01

Your printer's camera comes to the terminal, and AFC printers look the way
they do in Fluidd. Pane 5 swaps between the console and the webcam with
`Ctrl+T`, drawn sharp in Ghostty, Kitty and WezTerm and in block characters
anywhere else, following the cameras your printer has configured. Macro
output that Mainsail and Fluidd show in colour (AFC's lane status and banners)
is coloured here too, instead of arriving as raw HTML. Switching printers no
longer means quitting, a default printer connects at start, and sensors that
measure humidity show it.

### Added

- **Camera** (`Ctrl+T` in the console pane), contributed by djsplice. Live
  H.264, MJPEG, HLS and RTSP streams play through `ffmpeg`; other cameras
  show their snapshot. Ghostty, Kitty and WezTerm draw the picture sharp with
  the Kitty graphics protocol (inside tmux with `allow-passthrough on`); every
  other terminal draws quadrant or half blocks. `b` steps the picture through
  960, 640 and 480 pixels and the two block styles, trading detail for CPU.
  The pane follows the printer's `[webcam]` sections the way Mainsail, Fluidd
  and retrograde do: enabled cameras in their configured order (`n` for the
  next), each `service` played as intended, and `rotation`,
  `flip_horizontal`, `flip_vertical` and `target_fps` applied. How it works:
  [docs/CAMERA.md](docs/CAMERA.md).
- **Switch printers** with `P` from the dashboard, back to the connection
  screen without quitting.
- **A default printer:** `ctrl+s` on the connection screen makes the selected
  one connect at start. If it can't be reached, the screen opens and says why.
- **Humidity** in Temperatures for sensors that measure it (BME280, SHT3x,
  HTU21D…); the column appears only when one does. Suggested by jimmyjon711.

### Changed

- **Macro output in colour.** Text that macros write for Mainsail and Fluidd
  (AFC's `<span class=success--text>LOCKED AND LOADED</span>`, its ASCII
  READY banner) is coloured from your theme, with the tags gone — spans that
  run across lines included. `moontop gcode` prints it as plain text.
  Suggested by jimmyjon711.
- **AFC weights** round to two decimals: `609.24g`, not `609.2385417798512g`.
  Suggested by jimmyjon711.
- **Shorter installer address:**
  `curl -fsSL https://justinh-rahb.github.io/moontop/install.sh | sh` (the old
  raw GitHub one still works). When `~/.local/bin` isn't on your `PATH` yet,
  it now says how to pick it up.
- **`phosphor` and `amber`** draw unfocused pane borders brighter, so they
  survive CRT emulators such as Cool Retro Term.
- Release notes now include who contributed and how to install.

## [0.1.0] - 2026-11-01

The first release: a keyboard-driven terminal UI for Klipper printers,
through Moonraker, covering what you reach for in Mainsail or Fluidd.

### Added

- **Connecting:** a connection manager with your recent printers (the last
  one preselected), or `-host` / `$MOONRAKER_HOST`. Moonraker logins and API
  keys (`$MOONRAKER_API_KEY` too); refresh tokens and keys are kept per
  printer in an owner-only `hosts.json`, passwords never. `https://` for
  printers behind TLS.
- **Dashboard:** Files, Toolhead, Tuning, Temperatures, Console, AFC,
  Extruder, Outputs and Macros panes, laid out by a solver you can reorder
  and resize. Klippy state banner, restart and firmware restart (`F5` /
  `F6`), E-stop from anywhere (`Ctrl+X`).
- **Temperatures:** set targets in place, cooldown, presets (Mainsail's too),
  braille sparkline history backfilled from `server.temperature_store`.
- **Toolhead:** WASD jog, space / `c` for Z, typed absolute moves, homing,
  motors off, endstops, QGL or Z tilt when the printer has it.
- **Tuning and Extruder:** speed and flow, Z-offset babystep, velocity limits,
  tool select, extrude / retract, pressure advance, and filament load /
  unload detected from each printer's own macros.
- **Outputs:** fans, output pins and LEDs, built from your config.
- **AFC:** lanes from `AFC.lanes` with material, colour, weight, load / unload
  / eject.
- **Macros:** a browser of every macro (`F3` / `g`), a pinned Macros pane,
  parameter forms, and macro prompts (`action:prompt_*`).
- **Printing:** pause / resume / cancel, progress and ETA, exclude object
  (`O` / `F4`).
- **Jobs** (`J` / `F7`): a file manager (folders, slicer details, rename,
  copy, move, delete, upload, upload & print through a local file browser),
  print history and totals, and the job queue. The Files pane and the file
  manager follow changes on the printer as they happen.
- **Machine** (`M` / `F8`), **Software** (`U` / `F9`), **Bed mesh** (`B` /
  `F11`, a coloured grid of values, profiles, screws tilt, manual probe),
  **Logs** (`L` / `F12`) and **Config** (`E`, edit in `$EDITOR`).
- **Console:** tab completion with macro parameter hints, filters for
  heat-wait and TMC noise, history.
- **Setup** (`F2`): panes, columns, heaters, filters, jog steps, safety,
  presets and twelve colour schemes, from all-green `phosphor` to your
  terminal's own palette. Settings in `~/.config/moontop/config.toml`,
  per-printer ones in Moonraker's database.
- **Command line:** `moontop console` (an inline gcode console),
  `moontop gcode` (one-shot gcode with exit codes), `moontop upload` /
  `moontop print`, `moontop dump`, `moontop version`.
- Builds for Linux, macOS, Windows and FreeBSD on amd64 and arm64 (and armv7
  on Linux), an install script, and `go install`.

[Unreleased]: https://github.com/justinh-rahb/moontop/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/justinh-rahb/moontop/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/justinh-rahb/moontop/releases/tag/v0.1.0
