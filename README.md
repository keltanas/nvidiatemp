# nvidiatemp

**Quiet fans while the GPU works. Driver control when it rests.**

nvidiatemp is a small Linux daemon that takes NVIDIA GPU fan control into its own hands while the card is busy and hands it back to the driver when the card is idle. Instead of the driver's abrupt ramp-ups, fan speed follows a temperature curve you define — so the card stays cool without sounding like a jet engine.

[![CI](https://github.com/keltanas/nvidiatemp/actions/workflows/ci.yml/badge.svg)](https://github.com/keltanas/nvidiatemp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.27+](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)

Русская версия: [README_RU.md](README_RU.md) · 简体中文: [README_ZH.md](README_ZH.md)

## Why nvidiatemp

- **Quiet under load.** Fan speed follows a smooth temperature curve — no sudden jumps to 100%.
- **No display server.** The default NVML backend talks to the driver directly, without an X display or a sudo wrapper.
- **Safe by design.** Any daemon stop — a signal, a panic, or five failed polls in a row — returns every managed GPU to driver control.
- **Multi-GPU aware.** Every fan of every visible GPU is addressed explicitly, and each GPU can have its own curve.
- **No flapping.** A deadband and a pause between writes keep the fan from hunting around a single temperature.

## How it works

- Every `poll_interval` (2s by default), it reads temperature, power draw and the current fan speed of all visible GPUs in a single `nvidia-smi` call.
- **Manual mode.** When power draw exceeds the power threshold (50W by default), the GPU is switched to manual control and the fan speed follows the curve (piecewise-linear interpolation, clamped to [30, 100]).
- **Back to auto.** The card returns to driver control only when power is below the threshold **and** temperature is below the lower bound of the curve. The fan is then held at its minimum speed (30%) for `auto_delay` (5 minutes by default). If load returns during the cooldown — power above the threshold or temperature above the lower bound — the cooldown is cancelled and the curve takes over again.
- **Quiet hold.** An idle card (power below the threshold) is taken into manual mode for `quiet_hold` (3 minutes by default) and then handed back to the driver when either trigger fires:
  - the driver spins the fan up on its own (speed ≥ `quiet_threshold`, 30% by default) while the card is not hot (temperature below the lower bound of the curve);
  - the card stays at or above `quiet_temp` (50°C by default) continuously for `quiet_delay` (1 minute by default) — a slow idle warm-up, when the fan may still be running low.

  During the hold the speed follows the curve (30% at the lower bound). Load arriving earlier ends the hold; a fan-triggered hold also ends early if the card heats above the lower bound. Set `quiet_hold: 0` to disable both triggers.
- **No flapping.** A speed is written only when it differs from the current value by at least the deadband (5% by default) and no more often than once per `write_pause` (5s by default). Mode switches obey the same pause.
- **Backends.** Fan control goes through NVML by default (`backend: auto`): no X display, no sudo wrapper, and every fan of a GPU is addressed explicitly. `nvidia-settings` remains available as a fallback and can be forced with `backend: nvidia-settings`.
- **Safety.** On any daemon stop (SIGINT/SIGTERM, panic, five consecutive failed polls), every managed GPU is returned to auto mode.

## Requirements

- Linux with the NVIDIA driver. Reads go through `nvidia-smi` and need no root.
- Fan writes need root; the supplied systemd unit runs as root.
- The default NVML backend (`libnvidia-ml`) talks to the driver directly: no X display and no sudo wrapper.
- The `nvidia-settings` backend additionally needs an X display (`DISPLAY=:0`) and root, or passwordless sudo when started as a regular user (sudo is prepended automatically when euid ≠ 0).

## Building

```sh
go build -o nvidiatemp .
```

A single binary. You need Go 1.27+ and cgo: the NVML backend uses `github.com/NVIDIA/go-nvml`, which loads `libnvidia-ml` from the driver at runtime, so cross-compilation requires a C toolchain for the target platform. The other dependency is `gopkg.in/yaml.v3`.

Prebuilt `linux/amd64` and `linux/arm64` binaries are attached to every `v*` tag release — download the tarball and skip this section.

## Installation (systemd)

```sh
sudo install -m 0755 nvidiatemp /usr/local/bin/nvidiatemp
sudo cp deploy/nvidiatemp.example.yaml /etc/nvidiatemp.yaml   # edit if you like
sudo cp deploy/nvidiatemp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nvidiatemp
systemctl status nvidiatemp
```

The `XAUTHORITY` note applies only to `backend: nvidia-settings`: if the journal (`journalctl -u nvidiatemp`) reports X access errors, uncomment `Environment=XAUTHORITY=/home/<user>/.Xauthority` in the unit (or use `xhost +local:`). The default NVML backend needs no display.

## Subcommands

| Command | What it does |
| --- | --- |
| `nvidiatemp run` | Daemon: control loop until SIGINT/SIGTERM |
| `nvidiatemp status` | One-shot per-GPU report: temperature, fan speed, mode, target speed from the curve, power |
| `nvidiatemp set PCT` | All GPUs to manual mode at PCT percent ([30, 100]); no loop is started |
| `nvidiatemp set auto` | All GPUs back to driver (auto) control |
| `nvidiatemp curve` | Effective curve (common and per-index) and loop parameters |

The `-c <path>` flag sets the configuration file (default `/etc/nvidiatemp.yaml`) for `run`, `status`, `set` and `curve`. `run` also accepts `-v` to log every tick at debug level.

## Configuration

The format and defaults are documented in the comments of [`deploy/nvidiatemp.example.yaml`](deploy/nvidiatemp.example.yaml). Without a configuration file, the built-in defaults apply: curve `50→30, 60→40, 65→45, 70→55, 75→60, 80→70`, interval 2s, deadband 5%, pause 5s, power threshold 50W, 5-minute cooldown before returning to auto, quiet hold at 30% for 3 minutes (also when an idle card stays at 50°C for a minute), and backend `auto`.

The configuration is validated at startup: curve temperatures must strictly increase, speeds must be within [30, 100], the power threshold must be positive, and — when the quiet hold is enabled — the quiet threshold must be within (0, 100], `quiet_temp` must be positive and `quiet_delay` must be non-negative; the backend must be known. On error the process exits with code 1 before the control loop starts.

## Manual return to auto mode

If the daemon was stopped uncleanly (`kill -9`, power loss), a card may be left in manual mode. Return it to the driver manually:

```sh
nvidiatemp set auto
# or without nvidiatemp, for each GPU (index as reported by nvidia-smi):
sudo DISPLAY=:0 nvidia-settings -a "[gpu:0]/GPUFanControlState=0"
```

`nvidiatemp set auto` works through NVML and needs no X display; the raw `nvidia-settings` line above does.

Check the current mode:

```sh
DISPLAY=:0 nvidia-settings -q GPUFanControlState
nvidiatemp status                             # works without X (NVML backend)
```

## Rollback

```sh
sudo systemctl disable --now nvidiatemp   # on stop the daemon returns auto mode itself
sudo rm /etc/systemd/system/nvidiatemp.service /usr/local/bin/nvidiatemp
sudo systemctl daemon-reload
```

## License

MIT — see [LICENSE](LICENSE).
