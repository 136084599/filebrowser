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
