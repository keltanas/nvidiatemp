# nvidiatemp

A fan speed controller for NVIDIA GPUs: it takes cooling under its own control
when the card is busy (power draw above a threshold) and returns it to the
driver when idle.

Русская версия: [README_RU.md](README_RU.md)

## How it works

- Every `poll_interval` (default 2s) it reads temperature, power draw and the
  current fan speed of all visible GPUs in a single `nvidia-smi` call.
- Power above the threshold (default 50W) → the GPU is switched to manual mode
  and the fan speed follows a «temperature → speed» curve (piecewise-linear
  interpolation, clamped to [30, 100]).
- Return to auto mode happens only when power is below the threshold **and**
  temperature is below the lower bound of the curve. After that the fan is held
  at its minimum (30%) for `auto_delay` (default 5 minutes) and the switch to
  auto happens if no load appears in the meantime: any violation of the
  conditions (power above the threshold again, or temperature above the lower
  bound) cancels the cooldown and control follows the curve again.
- If the driver spins the fan up on its own while the GPU is in auto mode
  (speed ≥ `quiet_threshold`, default 30%) and the card is neither loaded
  (power below the threshold) nor hot (temperature below the curve's lower
  bound), the card is taken into manual mode at the curve speed (30% when
  idle) for `quiet_hold` (default 3 minutes), then handed back to the driver.
  Load or heat arriving sooner ends the hold early and normal curve control
  continues. `quiet_hold: 0` disables the interception.
- Anti-flapping: a speed is written only when it differs from the current one
  by at least the deadband (default 5%) and no more often than once per
  `write_pause` (default 5s); mode switches are subject to the same pause.
- Fan control goes through NVML by default (`backend: auto`), so no X display
  and no sudo wrapper are involved, and every fan of a GPU is addressed
  explicitly. `nvidia-settings` is still available as a fallback and can be
  forced with `backend: nvidia-settings`.
- Safety: on any daemon stop (SIGINT/SIGTERM, panic, 5 consecutive failed
  polls) all managed GPUs are returned to auto mode.

## Requirements

- Linux with the NVIDIA driver; reads go through `nvidia-smi` (no root
  needed).
- Writes go through NVML (`libnvidia-ml`) by default: no X display, no sudo.
  With `backend: nvidia-settings` they need X (`DISPLAY=:0`) and root, or
  passwordless sudo for interactive use (sudo is prepended automatically when
  euid ≠ 0).

## Building

```sh
go build -o nvidiatemp .
```

A single binary. The build needs cgo: the NVML backend uses
`github.com/NVIDIA/go-nvml`, which loads `libnvidia-ml` from the driver at
runtime, so cross-compilation requires a C toolchain for the target. The other
dependency is `gopkg.in/yaml.v3`.

Prebuilt `linux/amd64` and `linux/arm64` binaries are attached to every `v*`
tag release — download the tarball and skip this section.

## Installation (systemd)

```sh
sudo install -m 0755 nvidiatemp /usr/local/bin/nvidiatemp
sudo cp deploy/nvidiatemp.example.yaml /etc/nvidiatemp.yaml   # edit if you like
sudo cp deploy/nvidiatemp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nvidiatemp
systemctl status nvidiatemp
```

Only relevant with `backend: nvidia-settings`: if X access errors appear in the
journal (`journalctl -u nvidiatemp`), uncomment
`Environment=XAUTHORITY=/home/<user>/.Xauthority` in the unit (or use
`xhost +local:`). The default NVML backend needs no display.

## Subcommands

```
nvidiatemp run      daemon: control loop until SIGINT/SIGTERM
nvidiatemp status   one-shot per-GPU report: temperature, speed, mode, target speed from the curve, power
nvidiatemp set PCT  all GPUs to manual mode at PCT percent ([30, 100]); no loop is started
nvidiatemp set auto all GPUs back to auto (driver) control
nvidiatemp curve    effective curve (common + per-index) and loop parameters
```

The `-c <path>` flag sets the configuration file (default `/etc/nvidiatemp.yaml`)
for `run`, `status`, `set` and `curve`. `run` has `-v` — log every tick
(Debug level).

## Configuration

Format and defaults are documented in the comments of
[`deploy/nvidiatemp.example.yaml`](deploy/nvidiatemp.example.yaml). Without a
file, built-in defaults apply: curve `50→30, 60→40, 65→45, 70→55, 75→60,
80→70`, interval 2s, deadband 5%, pause 5s, power threshold 50W, auto-cooldown
5 minutes, quiet interception at 30% for 3 minutes, backend `auto`. The config
is validated at startup (curve
temperatures strictly increasing, speeds within [30, 100], power threshold > 0,
quiet threshold within (0, 100] when enabled, backend known) — on error the
process exits with code 1 before the loop starts.

## Manual return to auto mode

If the daemon was stopped uncleanly (`kill -9`, power loss), a card may be left
in manual mode. Return it to the driver manually:

```sh
nvidiatemp set auto
# or without nvidiatemp, for each GPU (index as reported by nvidia-smi):
sudo DISPLAY=:0 nvidia-settings -a "[gpu:0]/GPUFanControlState=0"
```

`nvidiatemp set auto` works through NVML and therefore needs no X display;
the raw `nvidia-settings` line above does.

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
