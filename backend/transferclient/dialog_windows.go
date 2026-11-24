//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func dialog(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", "[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new(); Add-Type -AssemblyName System.Windows.Forms; "+script)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	result, err := cmd.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(strings.TrimPrefix(string(result), "\ufeff"))
	if value == "" {
		return "", fmt.Errorf("已取消选择")
	}
	return value, nil
}
func chooseTask() (string, error) {
	return dialog("$d=New-Object System.Windows.Forms.OpenFileDialog; $d.Title='选择 FileBrowser 下载任务'; $d.Filter='FileBrowser 下载任务 (*.fbdownload)|*.fbdownload'; if($d.ShowDialog() -eq 'OK'){[Console]::Write($d.FileName)}")
}
func chooseDestination() (string, error) {
	return dialog("$d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description='选择保存文件夹的位置'; if($d.ShowDialog() -eq 'OK'){[Console]::Write($d.SelectedPath)}")
}
func enableUTF8() { syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001) }
func waitBeforeExit() {
	for _, arg := range os.Args[1:] {
		if arg == "-destination" || arg == "--destination" || strings.HasPrefix(arg, "-destination=") || strings.HasPrefix(arg, "--destination=") {
			return
		}
	}
	fmt.Println("按回车关闭窗口。")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
