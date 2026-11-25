# File Browser Quantum · LC Fix 10

在浏览器中管理服务器上的文件。可以上传、下载、搜索、预览、移动文件和创建分享。LC Fix 10 重点改进大文件传输、同名文件处理、媒体预览和目录索引。

[下载 Windows x64 整包](https://github.com/136084599/filebrowser-transfer-fixes/releases/download/v2.0.1-lcfix10/FileBrowser-LC-Fix-10-Windows-x64.zip) · [Release 和校验值](https://github.com/136084599/filebrowser-transfer-fixes/releases/tag/v2.0.1-lcfix10) · [安装、迁移与源码构建](LCFIX10.md)

## Windows 快速启动

整包已经包含服务器 EXE、示例配置和校验文件。基本文件管理无需另装上游、Go 或 Node.js。

1. 下载 ZIP，解压到一个可写的本地目录。
2. 在解压目录打开 PowerShell，执行下方命令。按提示设置首次登录的管理员密码。
3. 浏览器打开 [http://127.0.0.1:8080/](http://127.0.0.1:8080/)，用 `admin` 和刚设置的密码登录。服务窗口保持打开，把测试文件放进 `files` 即可在网页看到。

```powershell
if (-not (Test-Path .\config.yaml)) {
    Copy-Item .\config.example.yaml .\config.yaml
}
New-Item -ItemType Directory -Force -Path .\files | Out-Null
$pass = Read-Host '设置本机管理员密码' -AsSecureString
$env:FILEBROWSER_ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $pass).Password
try {
    .\filebrowser-transfer-fixes-lcfix10-windows-amd64.exe -c .\config.yaml
} finally {
    Remove-Item Env:FILEBROWSER_ADMIN_PASSWORD -ErrorAction SilentlyContinue
}
```

以后可以在同一目录用同一份 `config.yaml` 启动。示例使用本机地址和 `filebrowser.sqlite`。需生成视频缩略图或预览时，安装 FFmpeg 并加入 PATH。

## 功能与入口

| 功能 | 怎么使用 |
| --- | --- |
| 网页文件管理 | 从左侧选择文件源，在列表里打开、搜索、重命名、移动、复制或删除文件。分享和操作权限沿用 File Browser Quantum 的设置。 |
| 文件与文件夹上传 | 进入目标目录，打开上传面板或拖入文件。任务面板支持单项和批量暂停、继续、重试及取消。遇到同名文件时按提示处理。 |
| 上传断线与完整性 | 断线后优先在原任务面板继续或重试。修复版会校验接受的分块和最终文件，校验失败时保留原有目标文件。这些检查自动执行。 |
| 停止未完成上传 | 在上传面板点取消，服务端清理对应临时文件及本批新建的空目录，已完成文件保留。清理失败会提示再次重试。 |
| 浏览器下载 | 选文件后打开下载菜单。可以选择打包下载，或使用「浏览器直接下载（不压缩）」逐个交给浏览器。后者由浏览器管理进度和保存位置。 |
| 批量下载并保留目录 | 登录后选择「批量下载（不压缩，保留文件夹）」。浏览器可选目录时按提示操作。Windows 下载器方式先下载任务文件，拖到下载器上，再选保存位置。网络中断后可重新打开原任务继续。任务文件应留在自己电脑上。 |
| 预览和缩略图 | 点击支持的图片、视频或文档打开预览。LC Fix 10 改善了视频缩略图、并发预览复用和缓存异常时的重新生成。 |
| 同名处理与目录定位 | 上传、移动、复制或重命名遇到同名项目时，检查冲突提示再确认。新版改进了提示流程和返回列表时的定位。 |
| 索引、目录大小与配置 | 管理员按上游方法设置文件源和索引。此版包含索引持久化、目录大小更新及旧配置兼容修复。升级前在配置和数据库的副本上验证。 |

已验证 Windows x64 本机启动、首页和管理员登录。共享权限、真实大文件链路和反向代理需在各自部署上验收。此 Release 基于较早的上游提交，面向本机试用。对外部署优先查看上游当前受维护的版本与安全更新。 [File Browser Quantum 发布页](https://github.com/gtsteffaniak/filebrowser/releases)

## 源码与上游

`lcfix10` 是本 fork 的修复分支，基线是上游 [`aa4dd3a`](https://github.com/gtsteffaniak/filebrowser/commit/aa4dd3ae95ea8f54959e3df8187b4c02247601cb)。从源码构建或迁移已有库请看 [LCFIX10.md](LCFIX10.md)。用于审阅改动的补丁在 [filebrowser-transfer-fixes](https://github.com/136084599/filebrowser-transfer-fixes)，上游功能与部署选项见 [官方文档](https://filebrowserquantum.com/)。许可见 [LICENSE](LICENSE)。
