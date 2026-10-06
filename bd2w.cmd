@echo off
setlocal DisableDelayedExpansion
set "BD2W_ROOT=%~dp0"
set "BD2W_TOOL_DIR=%BD2W_ROOT%.build\tools"
set "BD2W_TOOL=%BD2W_TOOL_DIR%\bd2w.exe"
if exist "%BD2W_TOOL%" goto run

if not exist "%BD2W_TOOL_DIR%" mkdir "%BD2W_TOOL_DIR%"
if errorlevel 1 exit /b 1
set "BD2W_LOCK=%BD2W_TOOL_DIR%\.bootstrap.lock"
set "BD2W_ATTEMPTS=0"
:lock
if exist "%BD2W_TOOL%" goto run
mkdir "%BD2W_LOCK%" 2>nul
if errorlevel 1 goto wait
set "BD2W_TEMP=%BD2W_TOOL_DIR%\.bd2w-%RANDOM%-%RANDOM%.exe"
if exist "%BD2W_TOOL%" goto unlock
pushd "%BD2W_ROOT%go"
if errorlevel 1 goto failed
set "BD2W_PUSHED=1"
echo bd2w: compiling the build tool for first use...
set "BD2W_PREVIOUS_CGO=%CGO_ENABLED%"
set "GOCACHE=%BD2W_ROOT%go\.cache\go-build"
set "GOOS="
set "GOARCH="
for /f "delims=" %%A in ('go env GOHOSTOS') do set "GOOS=%%A"
for /f "delims=" %%A in ('go env GOHOSTARCH') do set "GOARCH=%%A"
if not defined GOOS goto failed
if not defined GOARCH goto failed
set "CGO_ENABLED=0"
go build -buildvcs=false -trimpath -o "%BD2W_TEMP%" ./build
if errorlevel 1 goto failed
move /y "%BD2W_TEMP%" "%BD2W_TOOL%" >nul
if errorlevel 1 goto failed
:unlock
if defined BD2W_PUSHED set "CGO_ENABLED=%BD2W_PREVIOUS_CGO%"
if defined BD2W_PUSHED popd
rmdir "%BD2W_LOCK%"
if errorlevel 1 exit /b 1
:run
"%BD2W_TOOL%" %*
exit /b %errorlevel%

:wait
set /a BD2W_ATTEMPTS+=1 >nul
if %BD2W_ATTEMPTS% geq 120 (
    echo bd2w: Bootstrap lock still held at "%BD2W_LOCK%"; check the first launcher. 1>&2
    exit /b 1
)
ping -n 2 127.0.0.1 >nul
goto lock

:failed
if defined BD2W_PUSHED popd
if defined BD2W_TEMP if exist "%BD2W_TEMP%" del /q "%BD2W_TEMP%"
rmdir "%BD2W_LOCK%"
echo bd2w: Build tool compilation failed. 1>&2
exit /b 1
