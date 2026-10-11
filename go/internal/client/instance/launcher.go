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
		body.WriteString("Option Explicit\r\nDim shell, environment\r\nSet shell = CreateObject(\"WScript.Shell\")\r\nSet environment = shell.Environment(\"Process\")\r\n")
		body.WriteString(`environment("BD2_CLIENT_INSTANCE_ID") = `)
		body.WriteString(quote(i.ID))
		body.WriteString("\r\n")
		body.WriteString(`environment("BD2_CLIENT_INSTANCE_ROOT") = `)
		body.WriteString(quote(i.Root))
		body.WriteString("\r\n")
		for _, key := range []string{"UNITY_PROXYSERVER", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "BD2_CLIENT_PROXY_URL"} {
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
	body.WriteString("unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy\nexport UNITY_PROXYSERVER='' BD2_CLIENT_PROXY_URL=''\nexport UNITY_NOPROXY='localhost,127.0.0.1,::1' NO_PROXY='localhost,127.0.0.1,::1' no_proxy='localhost,127.0.0.1,::1'\n")
	body.WriteString("export BD2_CLIENT_INSTANCE_ID=")
	body.WriteString(quote(i.ID))
	body.WriteString("\nexport BD2_CLIENT_INSTANCE_ROOT=")
	body.WriteString(quote(i.Root))
	body.WriteByte('\n')
	body.WriteString("cd ")
	body.WriteString(quote(installation.Root))
	body.WriteString("\nexec ")
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
