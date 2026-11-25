# File Browser Quantum · LC Fix 10

这是 File Browser Quantum 的下游 Windows 修复版。上游是 [gtsteffaniak/filebrowser](https://github.com/gtsteffaniak/filebrowser)，修复基线为 [aa4dd3ae95ea](https://github.com/gtsteffaniak/filebrowser/commit/aa4dd3ae95ea8f54959e3df8187b4c02247601cb)。`main` 保留上游历史，`lcfix10` 保存这版修复。补丁也保存在 [filebrowser-transfer-fixes](https://github.com/136084599/filebrowser-transfer-fixes)。许可沿用上游 [Apache-2.0](LICENSE)。

## 这一版处理了什么

- 上传中断、暂停或重试后的恢复，以及整文件校验。
- 大文件下载、原生 Windows 下载助手和下载取消后的处理。
- 同名冲突提示、目录返回定位、缩略图和预览等操作。

## Windows x64 下载与首次启动

[直接下载 Windows x64 整包](https://github.com/136084599/filebrowser-transfer-fixes/releases/download/v2.0.1-lcfix10/FileBrowser-LC-Fix-10-Windows-x64.zip)，或打开 [Release 附件与 SHA-256](https://github.com/136084599/filebrowser-transfer-fixes/releases/tag/v2.0.1-lcfix10)。整包包含下游服务 EXE、示例配置、文件校验值和操作说明，无需另装上游 File Browser。解压后在该目录把 `config.example.yaml` 复制为 `config.yaml`。首次启动后在浏览器使用。在该目录打开 PowerShell，运行下面的命令。

```powershell
New-Item -ItemType Directory -Force -Path .\files | Out-Null
$pass = Read-Host '为本次初始化设置管理员密码' -AsSecureString
$env:FILEBROWSER_ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $pass).Password
try {
    .\filebrowser-transfer-fixes-lcfix10-windows-amd64.exe -c .\config.yaml
} finally {
    Remove-Item Env:FILEBROWSER_ADMIN_PASSWORD -ErrorAction SilentlyContinue
}
```

打开 `http://127.0.0.1:8080/`，以默认管理员用户名 `admin` 和你刚设置的密码登录。示例配置只监听本机，文件目录为 `files`，SQLite 文件为 `filebrowser.sqlite`。首次初始化完成后，从同一目录使用同一配置重新启动。需要视频缩略图或预览时，另行安装 FFmpeg 并将其加入 PATH。

可用 `Get-FileHash .\filebrowser-transfer-fixes-lcfix10-windows-amd64.exe -Algorithm SHA256` 与整包中的 `SHA256SUMS.txt` 比对。这是下游自行构建的版本。

## 迁移与回滚

1. 记下当前程序版本、启动命令和实际配置的位置。停止旧实例后，备份旧 EXE、配置、数据库及其附属文件，并保护好共享目录。
2. 用数据库和配置的副本在独立目录、不同端口试运行。旧版数据库先按 [上游 v2 迁移指南](https://filebrowserquantum.com/en/docs/getting-started/v2/migration/) 判断迁移方式。保留数据库原本，不要让两个实例写同一份库。
3. 在自己的部署环境验证登录、用户权限、共享、上传、恢复和下载后，再切换正式服务。
4. 如需回滚，停止新实例，并一起恢复旧 EXE、旧配置和升级前的数据库副本。

## 从源码编译

拉取 `lcfix10` 分支，安装上游 `frontend/package.json` 和 `backend/go.mod` 所需工具链。在 Windows PowerShell 中从仓库根目录执行：

```powershell
Set-Location frontend
npm ci
npm exec -- vite build
Set-Location ..\backend
New-Item -ItemType Directory -Force -Path .\internal\web\embed | Out-Null
Copy-Item .\internal\web\dist\* .\internal\web\embed -Recurse -Force
go build -trimpath -buildvcs=false -ldflags '-s -w' -o .\internal\web\embed\filebrowser-download.exe ./transferclient
go build -trimpath -buildvcs=false -ldflags '-s -w' -o ..\filebrowser-transfer-fixes-lcfix10-windows-amd64.exe .
```

当前下载包由此分支的程序源文件构建。验证覆盖了 Windows x64 下的启动、首页与管理员登录。你的实际网关、磁盘、共享目录、用户权限和大文件链路仍需在自己的部署环境验收。包内不含生产配置、旧数据库或现场测试日志。

## 给上游贡献时怎么拆

LC Fix 10 同时改动了传输、预览、索引、原生下载器及迁移。在上游 `dev/v2.2.0` 上独立复现、拆分并测试的四个问题，已分别提交为草稿 PR。

| 修复 | 上游 PR |
| --- | --- |
| 同一视频缩略图的并发请求复用一次生成，单个请求取消时保留其他请求 | [#3457](https://github.com/gtsteffaniak/filebrowser/pull/3457) |
| 目录大小批量写入使用正确的 SQLite 参数顺序 | [#3458](https://github.com/gtsteffaniak/filebrowser/pull/3458) |
| 冲突确认等待处理结果时防止重复提交 | [#3459](https://github.com/gtsteffaniak/filebrowser/pull/3459) |
| 目录变空后立即使用缓存中的零值，同时保留缓存未命中时的数据库回退 | [#3460](https://github.com/gtsteffaniak/filebrowser/pull/3460) |

上游已修复上传写盘阶段的误暂停、暂停时的页面离开保护和部分目录索引更新。移动覆盖时的目录合并已有单独上游 PR。 [上传暂停 #3346](https://github.com/gtsteffaniak/filebrowser/pull/3346) · [导航保护 #3220](https://github.com/gtsteffaniak/filebrowser/pull/3220) · [索引 #3260](https://github.com/gtsteffaniak/filebrowser/pull/3260) · [目录合并 #3440](https://github.com/gtsteffaniak/filebrowser/pull/3440)

### 返回列表时的高亮

LC Fix 10 返回列表时会给刚离开的文件或目录加临时描边，保留文件操作原有的选中状态。上游普通列表已有返回定位，关闭预览后会选中最后查看的文件，返回上级时也会选中刚离开的子目录。 [上游预览导航 #3156](https://github.com/gtsteffaniak/filebrowser/pull/3156)

在上游 `dev/v2.2.0` 上，公开分享从子目录返回上级时漏掉了该定位。已用包含中文、空格、方括号、井号和百分号的目录名复现，并验证同一分享会定位、切换到另一分享时不会沿用刚离开的目录。两个回归用例、全套 745 项前端测试和类型检查均通过。 [分享返回定位补丁](https://github.com/136084599/filebrowser/tree/upstream-share-return-highlight)

后续可按上游当前上传协议，分别验证取消时清理未完成文件和上传结束的整文件校验。Windows 下载器可以独立说明使用场景。 [上游贡献指南](https://github.com/gtsteffaniak/filebrowser/blob/main/CONTRIBUTING.md)。
