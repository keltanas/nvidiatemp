# nvidiatemp

**高负载时安静散热，空闲时交还驱动。**

nvidiatemp 是一个小巧的 Linux 守护进程：显卡忙碌时，它接管 NVIDIA GPU 的风扇控制；显卡空闲时，再把控制权交还驱动。风扇转速按你设定的温度曲线变化，而不是随驱动突然飙升——显卡依然凉爽，却不会像喷气发动机一样轰鸣。

[![CI](https://github.com/keltanas/nvidiatemp/actions/workflows/ci.yml/badge.svg)](https://github.com/keltanas/nvidiatemp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.27+](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)

English version: [README.md](README.md) · Русская версия: [README_RU.md](README_RU.md)

## 为什么选择 nvidiatemp

- **高负载下更安静。** 转速随温度曲线平滑变化，不会突然飙到 100%。
- **无需图形服务。** 默认的 NVML 后端直接与驱动通信，不需要 X 显示，也没有 sudo 包装。
- **默认安全。** 无论因何停止——信号、panic 或连续五次轮询失败——所有受控 GPU 都会交还驱动。
- **支持多 GPU。** 每块可见显卡的每个风扇都被单独寻址，每块 GPU 还可以使用独立的曲线。
- **不会来回抖动。** 死区和写入间隔可防止风扇在某一温度附近反复升降。

## 工作原理

- 每隔 `poll_interval`（默认 2 秒），通过一次 `nvidia-smi` 调用读取所有可见 GPU 的温度、功耗和当前风扇转速。
- **手动模式。** 功耗超过功耗阈值（默认 50 W）时，GPU 切换到手动控制，转速按曲线变化（分段线性插值，限制在 [30, 100]）。
- **回到自动模式。** 只有当功耗低于阈值**且**温度低于曲线下界时，显卡才交还驱动。随后风扇会以最低转速（30%）运行 `auto_delay`（默认 5 分钟）。如果冷却期间负载再次出现——功耗高于阈值或温度高于下界——等待立即取消，重新按曲线控制。
- **静音接管。** 空闲显卡（功耗低于阈值）会进入手动模式并保持 `quiet_hold`（默认 3 分钟），然后在任一条件满足时交还驱动：
  - 显卡不热（温度低于曲线下界）时，驱动自行提高风扇转速（转速 ≥ `quiet_threshold`，默认 30%）；
  - 温度持续不低于 `quiet_temp`（默认 50 °C）达 `quiet_delay`（默认 1 分钟）——空闲时的缓慢升温，此时风扇可能仍未提速。

  接管期间转速按曲线变化（下界处为 30%）。若负载提前到来，接管提前结束；由风扇触发的接管在温度升至下界以上时也会提前结束。设置 `quiet_hold: 0` 可禁用这两个触发条件。
- **不会来回抖动。** 仅当目标转速与当前转速之差至少达到死区（默认 5%）时才写入，且同一 GPU 的两次写入间隔不小于 `write_pause`（默认 5 秒）。模式切换也受同一间隔限制。
- **后端。** 默认通过 NVML 控制风扇（`backend: auto`）：不需要 X 显示，也没有 sudo 包装，显卡的每个风扇都被单独寻址。`nvidia-settings` 仍可作为后备方案，通过 `backend: nvidia-settings` 强制启用。
- **安全。** 守护进程无论因何停止（SIGINT/SIGTERM、panic、连续五次轮询失败），所有受控 GPU 都会返回自动模式。

## 环境要求

- Linux 与 NVIDIA 驱动。读取通过 `nvidia-smi` 完成，无需 root。
- 写入风扇转速需要 root；随附的 systemd 单元默认以 root 运行。
- 默认的 NVML 后端（`libnvidia-ml`）直接与驱动通信：无需 X 显示，也没有 sudo 包装。
- `nvidia-settings` 后端还需要 X 显示（`DISPLAY=:0`）和 root；若以普通用户启动，则需要免密码 sudo（当 euid ≠ 0 时会自动加上 sudo）。

## 构建

```sh
go build -o nvidiatemp .
```

单个二进制文件。需要 Go 1.27+ 和 cgo：NVML 后端使用 `github.com/NVIDIA/go-nvml`，它在运行时从驱动加载 `libnvidia-ml`，因此交叉编译需要目标平台的 C 工具链。另一个依赖是 `gopkg.in/yaml.v3`。

每个 `v*` 标签的发布版本都附带 `linux/amd64` 和 `linux/arm64` 预编译二进制文件——下载压缩包即可跳过本节。

## 安装（systemd）

```sh
sudo install -m 0755 nvidiatemp /usr/local/bin/nvidiatemp
sudo cp deploy/nvidiatemp.example.yaml /etc/nvidiatemp.yaml   # 可按需修改
sudo cp deploy/nvidiatemp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nvidiatemp
systemctl status nvidiatemp
```

`XAUTHORITY` 说明仅适用于 `backend: nvidia-settings`：如果日志（`journalctl -u nvidiatemp`）中出现 X 访问错误，请取消单元文件中 `Environment=XAUTHORITY=/home/<user>/.Xauthority` 的注释（或执行 `xhost +local:`）。默认的 NVML 后端不需要显示。

## 子命令

| 命令 | 作用 |
| --- | --- |
| `nvidiatemp run` | 守护进程：运行控制循环，直到收到 SIGINT/SIGTERM |
| `nvidiatemp status` | 单次输出每块 GPU 的状态：温度、风扇转速、模式、曲线目标转速、功耗 |
| `nvidiatemp set PCT` | 将所有 GPU 设为手动模式，转速为 PCT（[30, 100]）；不启动控制循环 |
| `nvidiatemp set auto` | 将所有 GPU 交还驱动（自动）控制 |
| `nvidiatemp curve` | 输出实际生效的曲线（通用曲线与按 GPU 覆盖）及控制循环参数 |

`-c <path>` 标志为 `run`、`status`、`set` 和 `curve` 指定配置文件（默认 `/etc/nvidiatemp.yaml`）。`run` 还支持 `-v`，以 debug 级别记录每个轮询周期。

## 配置

格式和默认值见 [`deploy/nvidiatemp.example.yaml`](deploy/nvidiatemp.example.yaml) 中的注释。没有配置文件时使用内置默认值：曲线 `50→30, 60→40, 65→45, 70→55, 75→60, 80→70`，轮询间隔 2 秒，死区 5%，写入间隔 5 秒，功耗阈值 50 W，返回自动模式前冷却 5 分钟，静音接管以 30% 保持 3 分钟（空闲显卡持续处于 50 °C 达一分钟时同样适用），后端为 `auto`。

启动时会校验配置：曲线温度必须严格递增，转速必须在 [30, 100] 范围内，功耗阈值必须为正数；启用静音接管时，接管阈值必须在 (0, 100] 范围内，`quiet_temp` 必须为正数，`quiet_delay` 必须非负；后端必须是已知值。出错时进程会在控制循环启动前以退出码 1 结束。

## 手动返回自动模式

如果守护进程非正常停止（`kill -9`、断电），显卡可能停留在手动模式。可手动交还驱动：

```sh
nvidiatemp set auto
# 或者不使用 nvidiatemp，对每块 GPU 执行（索引与 nvidia-smi 一致）：
sudo DISPLAY=:0 nvidia-settings -a "[gpu:0]/GPUFanControlState=0"
```

`nvidiatemp set auto` 通过 NVML 工作，不需要 X 显示；上面的 `nvidia-settings` 命令则需要。

检查当前模式：

```sh
DISPLAY=:0 nvidia-settings -q GPUFanControlState
nvidiatemp status                             # 无需 X（NVML 后端）
```

## 回滚

```sh
sudo systemctl disable --now nvidiatemp   # 停止时守护进程会自动恢复自动模式
sudo rm /etc/systemd/system/nvidiatemp.service /usr/local/bin/nvidiatemp
sudo systemctl daemon-reload
```

## 许可证

MIT，详见 [LICENSE](LICENSE)。
