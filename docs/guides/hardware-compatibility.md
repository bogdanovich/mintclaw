# MintClaw Hardware And Release Compatibility

MintClaw publishes a deliberately bounded stable release matrix. A Go port or a
community-reported device is not automatically a supported release target.

| Artifact | Stable release targets | Coding qualification |
| --- | --- | --- |
| `mintclaw` and `mintclaw-launcher` archives | Linux AMD64, ARM64, ARMv7; macOS AMD64, ARM64; Windows AMD64 | Interactive TUI and process lifecycle are qualified on Linux and macOS. Native Windows supports headless coding; its interactive PTY remains unqualified. |
| `mintclaw-node` archive | Linux AMD64/ARM64; macOS AMD64/ARM64 | Remote coding companions are admitted only where their separately documented target/profile policy allows them. |
| Container images | Linux AMD64/ARM64 | Headless gateway/runtime deployment. |

Linux ARM64/ARMv7 and macOS AMD64 release builds are compile-qualified. Linux
AMD64 and macOS ARM64 receive the broadest native runtime coverage. Windows
AMD64 additionally runs native path, lock, worktree, worker-process, and
headless-coding tests. Unsupported or unqualified combinations are not emitted
by the stable release configuration.

The inventory below records historical or community hardware reports inherited
from PicoClaw. It can be useful when building from source, but does not expand
the stable MintClaw release matrix above.

---

## 1. Community-Reported Chip Compatibility

### x86

| Vendor | Chip | Notes |
|--------|------|-------|
| Intel | Any x86 CPU (i386+) | All desktop/server/laptop processors |
| AMD | Any x86 CPU | All desktop/server/laptop processors |

### ARM

| Sub-arch | Typical Chips | Notes |
|----------|--------------|-------|
| ARMv6 | [BCM2835](https://www.raspberrypi.com/documentation/computers/processors.html#bcm2835) (Raspberry Pi 1/Zero) | Single-core ARM1176JZF-S |
| ARMv7 | [Allwinner V3s](https://linux-sunxi.org/V3s) | Single-core Cortex-A7, used in LicheePi Zero |
| ARM64 | [Allwinner H618](https://linux-sunxi.org/H618) | Quad-core Cortex-A53, used in Orange Pi Zero 3 |
| ARM64 | [BCM2711](https://www.raspberrypi.com/documentation/computers/processors.html#bcm2711) (Raspberry Pi 4) | Quad-core Cortex-A72 |
| ARM64 | [BCM2712](https://www.raspberrypi.com/documentation/computers/processors.html#bcm2712) (Raspberry Pi 5) | Quad-core Cortex-A76 |
| ARM64 | [AX630C](https://www.axera-tech.com/) (爱芯元智) | Dual-core Cortex-A53 + NPU, used in NanoKVM-Pro / MaixCAM2 |

### RISC-V (riscv64)

| Vendor | Chip | Core | Notes |
|--------|------|------|-------|
| [SOPHGO (算能)](https://www.sophgo.com/) | SG2002 | C906 @ 1GHz | 256MB DDR3 on-chip, used in LicheeRV-Nano / NanoKVM / MaixCAM |
| [Allwinner (全志)](https://www.allwinnertech.com/) | V861 | Dual C907 | 128MB DDR3L on-chip, 1 TOPS NPU, 4K AI camera SiP |
| [Allwinner (全志)](https://www.allwinnertech.com/) | V881 | C907 | RISC-V AI camera series |
| [Arterytek (匠芯创)](https://www.arterytek.com/) | D213 | RISC-V | Used in HaaS506-LD1 industrial RTU |
| [SpacemiT (进迭)](https://www.spacemit.com/) | K1 | 8x X60 @ 1.8GHz | Used in Milk-V Jupiter, BananaPi BPI-F3 |
| [SpacemiT (进迭)](https://www.spacemit.com/) | K3 | 8x X100 @ 2.5GHz | RVA23 compliant, 1024-bit RVV, FP8 AI inference |
| [Zhihe (知合)](https://www.zhihe-tech.com/) | A210 | High-perf RISC-V | 8-core, 16MB L3 cache, desktop-class |
| [Canaan (嘉楠)](https://www.canaan-creative.com/) | K230 | Dual C908 @ 1.6GHz | 6 TOPS KPU, used in CanMV-K230 |

### MIPS

| Vendor | Chip | Notes |
|--------|------|-------|
| MediaTek | [MT7620](https://www.mediatek.com/products/home-networking/mt7620) | MIPS24KEc @ 580MHz, used in many OpenWrt routers (e.g. Xiaomi Router 3G) |

### LoongArch (loong64)

| Vendor | Chip | Notes |
|--------|------|-------|
| [Loongson (龙芯)](https://www.loongson.cn/) | 3A5000 | Quad-core LA464 @ 2.5GHz, desktop/workstation |
| [Loongson (龙芯)](https://www.loongson.cn/) | 3A6000 | Quad-core 4C/8T @ 2.5GHz, IPC comparable to Intel 10th gen |
| [Loongson (龙芯)](https://www.loongson.cn/) | 2K1000LA | Dual-core @ 1GHz, industrial/IoT applications |

---

## 2. Community-Reported Products (by release date)

Consumer products, routers, and industrial devices reported by contributors.

| Year | Product | Arch | SoC | RAM | Category |
|------|---------|------|-----|-----|----------|
| 2009 | Nokia N900 | ARM (A8) | OMAP3430 | 256MB | Smartphone |
| 2012 | Samsung Galaxy Note 10.1 (N8000) | ARM (A9) | Exynos 4412 | 2GB | Tablet |
| 2016 | Xiaomi Router 3G (小米路由器3G) | MIPS | MT7620 | 256MB | Router (OpenWrt) |
| 2018 | Phicomm N1 (斐讯N1) | ARM64 (A53) | S905D | 2GB | TV Box / Home Server |
| 2019 | Xiaomi AI Speaker (小爱音箱) | ARM64 (A53) | — | 256MB | Smart Speaker |
| 2024 | [NanoKVM](https://wiki.sipeed.com/hardware/en/kvm/NanoKVM/introduction.html) | RISC-V | SG2002 | 256MB | IP-KVM |
| 2025 | HaaS506-LD1 | RISC-V | D213 | 128MB | Industrial RTU |
| 2025 | [NanoKVM-Pro](https://wiki.sipeed.com/hardware/en/kvm/NanoKVM_Pro/introduction.html) | ARM64 (A53) | AX630C | 1GB | Pro IP-KVM |
| 2026 | [MaixCAM2](https://wiki.sipeed.com/hardware/en/maixcam/index.html) | ARM64 (A53) | AX630C | 1/4GB | 4K AI Camera |

---

## 3. Community-Reported Development Boards (by release date)

| Year | Board | Arch | SoC | RAM | Buy Link |
|------|-------|------|-----|-----|----------|
| 2012 | [Raspberry Pi 1 Model B](https://www.raspberrypi.com/products/) | ARMv6 | BCM2835 | 512MB | — |
| 2015 | [Raspberry Pi 2 Model B](https://www.raspberrypi.com/products/raspberry-pi-2-model-b/) | ARMv7 (A7) | BCM2836 | 1GB | — |
| 2015 | [Raspberry Pi Zero](https://www.raspberrypi.com/products/raspberry-pi-zero/) | ARMv6 | BCM2835 | 512MB | — |
| 2016 | [Raspberry Pi 3 Model B](https://www.raspberrypi.com/products/raspberry-pi-3-model-b/) | ARM64 (A53) | BCM2837 | 1GB | — |
| 2017 | [LicheePi Zero](https://wiki.sipeed.com/hardware/en/lichee/Zero/Zero.html) | ARMv7 (A7) | Allwinner V3s | 64MB | [Sipeed](https://sipeed.com/) |
| 2019 | [Raspberry Pi 4 Model B](https://www.raspberrypi.com/products/raspberry-pi-4-model-b/) | ARM64 (A72) | BCM2711 | 1~8GB | [RPi](https://www.raspberrypi.com/) |
| 2023 | [Raspberry Pi 5](https://www.raspberrypi.com/products/raspberry-pi-5/) | ARM64 (A76) | BCM2712 | 2~8GB | [RPi](https://www.raspberrypi.com/) |
| 2024 | [LicheeRV-Nano](https://wiki.sipeed.com/hardware/en/lichee/RV_Nano/1_intro.html) | RISC-V | SG2002 | 256MB | [AliExpress](https://www.aliexpress.com/item/1005006519668532.html) |
| 2024 | [MaixCAM-Pro](https://wiki.sipeed.com/hardware/en/maixcam/index.html) | RISC-V | SG2002 | 256MB | [Sipeed](https://sipeed.com/) |
| 2024 | [Milk-V Duo 64M](https://milkv.io/docs/duo/getting-started/duo) | RISC-V | CV1800B | 64MB | [Milk-V](https://milkv.io/) |
| 2024 | [CanMV-K230](https://developer.canaan-creative.com/k230_canmv/en/main/) | RISC-V | K230 | 512MB | [Canaan](https://www.canaan-creative.com/) |

---

## 4. Also Works On

### Android Phones (via Termux)

Any ARM64 Android phone (2015+) with 1GB+ RAM. Install [Termux](https://github.com/termux/termux-app), use `proot` to run MintClaw.

> See the [Android Termux Guide](android-termux.md) for setup instructions.

### Desktop / Server / Cloud

| Platform | Notes |
|----------|-------|
| x86_64 Linux | Stable native archive and container image |
| ARM64 Linux | Stable native archive and container image |
| ARMv7 Linux | Stable native archive |
| x86_64 Windows | Stable native archive; headless coding is qualified |
| macOS (Intel / Apple Silicon) | Stable native archives |
| Docker | Linux AMD64/ARM64; see the [Docker Guide](docker.md) |
| Other Go targets | Build-from-source experiments; no stable artifact or support claim |

---

## 5. Minimum Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| RAM | 10MB free | 32MB+ free |
| Storage | 20MB (binary) | 50MB+ (with workspace) |
| CPU | Any (single core 0.6GHz+) | — |
| OS | Linux (kernel 3.x+) | Linux 5.x+ |
| Network | Required (for LLM API calls) | Ethernet or WiFi |

---

## 6. How to Test & Contribute

```bash
# 1. Download for your architecture
wget https://github.com/bogdanovich/mintclaw/releases/latest/download/mintclaw_Linux_arm64.tar.gz
tar xzf mintclaw_Linux_arm64.tar.gz

# 2. Initialize
./mintclaw onboard

# 3. Test
./mintclaw agent -m "Hello, what board am I running on?"
```

Stable archives: `Linux_x86_64`, `Linux_arm64`, `Linux_armv7`,
`Darwin_x86_64`, `Darwin_arm64`, and `Windows_x86_64`.

### Add Your Hardware

1. Fork this repo
2. Add your chip / product / board to the appropriate table
3. Include: name, arch, SoC, RAM, year, and a link if available
4. Submit a PR

Hardware vendors: want to add official support or co-promote? Open an issue or reach out via [Discord](https://discord.gg/V4sAZ9XWpN).
