package instance

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	clientlayout "bd2server/internal/client/layout"
)

func (i Instance) WriteLauncher() (string, error) {
	installation, err := clientlayout.Resolve(i.GameDirectory)
	if err != nil {
		return "", err
	}
	args := []string{"-screen-fullscreen", "0", "-screen-width", "1280", "-screen-height", "720", "-logFile", filepath.Join(i.Root, "logs", "Player.log"), "-crash-report-folder", filepath.Join(i.Root, "logs", "crashes")}
	if installation.Kind == clientlayout.Windows {
		quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
		var command strings.Builder
		command.WriteByte('"')
		command.WriteString(installation.Executable)
		command.WriteByte('"')
		for _, arg := range args {
			command.WriteString(` "`)
			command.WriteString(arg)
			command.WriteByte('"')
		}
		var body strings.Builder
		body.WriteString("Option Explicit\r\nDim shell, environment, files, processes, process, previousLog, pendingLog\r\nSet shell = CreateObject(\"WScript.Shell\")\r\nSet environment = shell.Environment(\"Process\")\r\n")
		body.WriteString(`Set processes = GetObject("winmgmts:\\.\root\cimv2").ExecQuery("SELECT ExecutablePath FROM Win32_Process WHERE Name = 'BrownDust II.exe'")`)
		body.WriteString("\r\nFor Each process In processes\r\nIf Not IsNull(process.ExecutablePath) Then\r\nIf LCase(process.ExecutablePath) = LCase(")
		body.WriteString(quote(installation.Executable))
		body.WriteString(") Then\r\nMsgBox \"This client instance is already running.\", 48, \"BD2\"\r\nWScript.Quit 1\r\nEnd If\r\nEnd If\r\nNext\r\nSet files = CreateObject(\"Scripting.FileSystemObject\")\r\npreviousLog = ")
		body.WriteString(quote(filepath.Join(i.Root, "logs", "Player.log")))
		body.WriteString("\r\nIf files.FileExists(previousLog) Then\r\npendingLog = files.BuildPath(files.GetParentFolderName(previousLog), \"bd2-previous.\" & files.GetTempName & \".Player-prev.log\")\r\nfiles.MoveFile previousLog, pendingLog\r\nEnd If\r\n")
		for _, key := range []string{"UNITY_PROXYSERVER", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			body.WriteString("environment(")
			body.WriteString(quote(key))
			body.WriteString(") = \"\"\r\n")
		}
		for _, key := range []string{"UNITY_NOPROXY", "NO_PROXY", "no_proxy"} {
			body.WriteString("environment(")
			body.WriteString(quote(key))
			body.WriteString(") = \"localhost,127.0.0.1,::1\"\r\n")
		}
		body.WriteString(`shell.CurrentDirectory = `)
		body.WriteString(quote(installation.Root))
		body.WriteString("\r\n")
		body.WriteString(`shell.Run `)
		body.WriteString(quote(command.String()))
		body.WriteString(", 1, False\r\n")
		path := filepath.Join(i.Root, "Start "+i.ID+".vbs")
		characters := utf16.Encode([]rune(body.String()))
		encoded := make([]byte, 2+2*len(characters))
		encoded[0], encoded[1] = 0xff, 0xfe
		for index, character := range characters {
			binary.LittleEndian.PutUint16(encoded[2+2*index:], character)
		}
		return path, os.WriteFile(path, encoded, 0o600)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	var body strings.Builder
	body.WriteString("#!/bin/sh\nset -eu\n")
	body.WriteString("unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy\nexport UNITY_PROXYSERVER=''\nexport UNITY_NOPROXY='localhost,127.0.0.1,::1' NO_PROXY='localhost,127.0.0.1,::1' no_proxy='localhost,127.0.0.1,::1'\n")
	body.WriteString("cd ")
	body.WriteString(quote(installation.Root))
	body.WriteString("\nprevious_log=")
	body.WriteString(quote(filepath.Join(i.Root, "logs", "Player.log")))
	body.WriteString("\nif [ -f \"$previous_log\" ]; then\n  pending_log=\"$(dirname \"$previous_log\")/bd2-previous.$(date +%Y%m%d%H%M%S).$$.Player-prev.log\"\n  ln \"$previous_log\" \"$pending_log\"\n  rm \"$previous_log\"\nfi\nexec ")
	body.WriteString(quote(installation.Executable))
	for _, arg := range args {
		body.WriteByte(' ')
		body.WriteString(quote(arg))
	}
	body.WriteString("\n")
	path := filepath.Join(i.Root, "Start "+i.ID+".command")
	if err := os.WriteFile(path, []byte(body.String()), 0o700); err != nil {
		return "", fmt.Errorf("write client launcher: %w", err)
	}
	return path, nil
}
