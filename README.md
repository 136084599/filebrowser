# File Browser Quantum · LC Fix 10

面向 Windows x64 的 File Browser Quantum 传输修复版。Windows x64 用户可直接下载 Release 整包。包里的 EXE 就是修好的 File Browser 服务，解压并配置后即可在本机运行，无需先安装上游。

**[Windows x64 Release 和 EXE](https://github.com/136084599/filebrowser-transfer-fixes/releases/tag/v2.0.1-lcfix10)** · [安装、迁移、回滚与编译](LCFIX10.md) · [下载校验](https://github.com/136084599/filebrowser-transfer-fixes/releases/download/v2.0.1-lcfix10/SHA256SUMS.txt)

## 修复内容

- 改进上传暂停与恢复、整文件校验和下载取消处理。
- 提供原生 Windows 下载助手，并改善同名提示、目录返回定位及预览。
- 整包包含服务 EXE、示例配置、操作说明和 SHA-256 校验文件。

## 启动和验证

把 `config.example.yaml` 复制为 `config.yaml`，按[启动说明](LCFIX10.md#windows-x64-下载与首次启动)设置初始管理密码，然后运行 EXE。示例配置只监听 `127.0.0.1:8080`。本次重编译通过了 Windows 下的本机启动、首页和管理员登录检查。生产升级前先用旧数据库的副本验证实际权限、文件共享和传输。

## 上游与维护

这是 [File Browser Quantum](https://github.com/gtsteffaniak/filebrowser) 的 fork。LC Fix 10 从上游 [`aa4dd3a`](https://github.com/gtsteffaniak/filebrowser/commit/aa4dd3ae95ea8f54959e3df8187b4c02247601cb) 分出。`lcfix10` 是这一版的维护分支，`main` 用于对照上游。补丁另存于 [filebrowser-transfer-fixes](https://github.com/136084599/filebrowser-transfer-fixes)。许可沿用上游 [Apache-2.0](LICENSE)。
