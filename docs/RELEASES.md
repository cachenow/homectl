# Release 文件选择

HomeCTL Release 同时提供普通二进制和可选 UPX 压缩版。

## 普通版

文件名不带 `-upx`。

推荐绝大多数用户使用：

- 兼容性最好
- 启动路径最简单
- 更容易调试和排查问题
- 不容易触发某些安全软件对 packed executable 的误报

Go 构建本身已经使用 `-trimpath` 和 `-ldflags="-s -w"` 减小文件体积。

## UPX 版

文件名带：

```text
-upx
```

适用于：

- Flash/eMMC 空间很小的嵌入式 Linux
- 下载带宽或固件镜像空间特别有限

Release 中的 UPX 版是在同一个正常构建产物上额外执行高压缩选项并通过 `upx -t` 完整性测试。普通产物始终保留；某个架构如果 UPX 压缩/测试失败，只会缺少该架构的 UPX 附加包，不影响普通 Release。

UPX 主要节省**磁盘/传输体积**，不代表运行时内存会按相同比例下降。启动时需要在内存中解包，也可能被部分安全产品标记为 packed binary。

## 架构

Agent 默认提供：

```text
linux-amd64
linux-arm64
linux-armv7
```

Server 默认提供：

```text
linux-amd64
linux-arm64
```

KVM/QEMU 虚拟机根据 Guest OS 的 CPU 架构选择即可。

## 下载后验证

把 `SHA256SUMS` 与下载的产物放在同一目录：

```bash
sha256sum -c SHA256SUMS
```

`SHA256SUMS` 可能同时列出未下载的其他架构；只校验单个文件时可执行：

```bash
grep 'homectl-agent-v2.0.0-linux-amd64.tar.gz$' SHA256SUMS | sha256sum -c -
```

安装前还可以核对版本：

```bash
./homectl-agent -version
./homectl-server -version
```
