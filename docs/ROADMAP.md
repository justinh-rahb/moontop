# Moontop roadmap

Updated Nov 2, 2026, after v0.2.0. This is the plan from here: where Moontop stands, what comes next and in what order, and what's deliberately out. What shipped and when is in [CHANGELOG.md](../CHANGELOG.md); how the code is put together is in [AGENTS.md](../AGENTS.md).

## Where it stands

Moontop is a Moonraker client for the terminal: a dashboard, a set of full-screen tools, a line console and scriptable commands, with no SSH and nothing installed on the printer. The October gap audit against Mainsail (40 gaps over Phases 13–25) is closed except for the items carried into the backlog below.

| Area | Status |
| --- | --- |
| Dashboard | Files, Toolhead, Tuning, Temperatures, Console / camera, AFC, Extruder, Outputs, Macros; layout solver, setup screen, 12 colour schemes |
| Full-screen tools | Macros, exclude object, Jobs (file manager, history, queue), Machine (host, MCUs, services, power devices), Software, Bed mesh, Logs, Config |
| Connecting | Connection manager with history and a default printer, `P` to switch; Moonraker login and API keys |
| Camera | Pane 5, following Moonraker's `[webcam]` config; Kitty graphics or block characters ([CAMERA.md](CAMERA.md)) |
| Command line | `console`, `gcode`, `upload` / `print`, `dump`, `version` |
| Releases | v0.1.0 and v0.2.0; builds for Linux, macOS, Windows and FreeBSD; changelog-driven releases ([RELEASING.md](RELEASING.md)); website on GitHub Pages |
| Tests | Every feature against the fake Moonraker; race detector on Linux, macOS and Windows in CI |

**Verified on hardware:** the dashboard, Toolhead, Temperatures, Files and the full-screen tools on Justin's Snapmaker U1 and Qidi Q2; AFC display and console output on jimmyjon711's AFC printer (Box Turtle, ViViD, EMU, OpenAMS, ACE); the camera on djsplice's printer in Ghostty.

**Not yet verified on hardware:** AFC load / unload / eject, extrude / retract, filament load / unload macros, LED colour, power-device switching, upload & print, sign-in against a Moonraker with `force_logins`, and the camera's MJPEG, HLS and RTSP paths. Each is covered by fake-Moonraker tests; the [hardware checklist](#hardware-checklist) is how they get confirmed.

## Next: 0.3

In order. Hardening first: Moontop is now used on printers its author doesn't own, and these are the places a dropped message or an unexpected macro does real damage. The features after it are Justin's list.

### 1. Hardening

From an outside review of v0.2.0 ([notes](#review-notes)).

| Item | Why | Done when |
| --- | --- | --- |
| **Resync after lost notifications** | The client drops status diffs when the UI falls 256 behind (`internal/moonraker/client.go`). A lost diff leaves a field stale until it next changes. | The client counts drops; any drop triggers one `printer.objects.query` of the subscribed objects, merged like the subscribe snapshot. Test: flood the fake server, check the UI matches its final state. |
| **Connection lifecycle under stress** | Reconnects during in-flight RPCs, Klippy restarts, `P` while a stream or upload runs, quitting mid-upload. | Race-detector tests for each: no leaked goroutines (ffmpeg, readers), no RPC waits forever, no send after close. |
| **Credentials** | Tokens and keys live in an owner-only `hosts.json`. | Permission tests per OS (Windows ACLs differ); redirect stripping (done in Unreleased); keys never in logs, the console or `moontop dump`. |
| **Physical-action limits** | Jogs already refuse unhomed axes and out-of-range moves. Apply the same rule to every physical action. | Heater targets capped at `max_temp` from `configfile`; extrusion refused below `min_extrude_temp`; AFC and macro actions that move or heat confirm when a print is running. |

### 2. Printing

| Item | Notes |
| --- | --- |
| **Layers** | `print_stats.info.current_layer` / `total_layer` (set by `SET_PRINT_STATS_INFO`), else estimated from Z and the file's layer height, as Mainsail does. Shown in the footer and Status. |
| **Live speed and flow** | `motion_report.live_velocity` and `live_extruder_velocity`, flow in mm³/s from the filament diameter. In Tuning beside the requested factors. |
| **ETA settings** | Mainsail's estimate sources: file (progress), filament used vs total, slicer estimate, and their average. A setup option for which one the footer shows, and whether to show the finish time or time remaining. |

### 3. Temperatures

| Item | Notes |
| --- | --- |
| **Graph modes** | The pane's History column has one mode (braille sparkline, 5 min). Add a taller block chart and a target overlay, cycled with a key. |
| **Full-page view** | A screen (key TBD) with a large multi-heater chart over the temperature store's 20 minutes, a legend, and the same set / off / preset keys as the pane. |

### 4. Camera

| Item | Notes |
| --- | --- |
| **Matrix view** | `m` on the camera tiles every enabled camera at once, each at a smaller decode width. One ffmpeg per live camera, so the 480 rung or snapshots by default. |

### 5. Power devices

The Machine screen (`M` / `F8`) already lists and switches Moonraker power devices. Next: reach them without leaving the dashboard (a key that opens a small device menu), and show a printer's PSU state in the title when it's off, with the option to switch it on from the connection screen before connecting to Klipper.

### 6. Website

| Item | Notes |
| --- | --- |
| **Themes** | A strip of the same dashboard in each of the 12 schemes, switchable on the page. |
| **Camera** | A short screen recording of the camera pane (WebM / MP4, muted, autoplay on view), with a still for reduced-motion. |

## Backlog

Carried over from the gap audit and earlier plans, not scheduled:

- **Object browser** (old Phase 26): a live, filterable tree of every Klipper object, watch a field, copy its `printer.<object>.<field>` path. Idea from [SocketKeys](https://github.com/N3MI-DG/SocketKeys) via jimmyjon711.
- **Display options:** ASCII glyphs and square or ASCII borders, for fonts and CRT emulators that mangle box-drawing and arrows (seen in Cool Retro Term).
- **Key remapping**, from the keymap registry.
- **Spoolman** and **timelapse** panels.
- **Firmware retraction** settings in Extruder.

## Out of scope

The gcode 3D viewer and file thumbnails, a multi-printer farm view (Moontop is one printer at a time, switched with `P`), drag-and-drop upload, and MMU / Happy Hare and LED-effect panels until someone with the hardware asks.

## Hardware checklist

Run before a release on any printer that has the feature, and note the result in the release PR or issue:

- [ ] AFC: load, unload, eject a lane; set material, weight, colour; tool change
- [ ] Extruder: extrude and retract at temperature; refused when cold
- [ ] Filament load / unload with the printer's own macros
- [ ] Outputs: fan speed, PWM pin, LED colour
- [ ] Power device on / off, including `locked_while_printing`
- [ ] Upload, and upload & print, from the file manager and `moontop print`
- [ ] Sign in with a user and with an API key on a Moonraker with `force_logins: True`; restart Moonraker after an hour (token refresh)
- [ ] Camera: one camera of each service the printer has; `rotation` and flips
- [ ] E-stop, then `F6` firmware restart, from the console

## Review notes

An outside review of v0.2.0 (Nov 2) singled out the native-client boundary, the CLI and `moontop dump` fixtures, the fake-Moonraker test architecture, and the keyboard-first design as the right foundations, and suggested hardening before more features. Its five points are the Hardening table, plus the hardware checklist above. It also flagged this document's old opening (the October audit of a 2,769-line, five-file codebase) as misleading now; the audit and the UI plan it led to are in git history (`git show v0.2.0:docs/ROADMAP.md`, `git show v0.2.0:docs/UI-PLAN.md`).
