@echo off
setlocal DisableDelayedExpansion
set "BD2W_ROOT=%~dp0"
if /I "%~1"=="check-csharp" goto check_csharp
set "BD2W_TOOL_DIR=%BD2W_ROOT%.build\tools"
set "BD2W_TOOL=%BD2W_TOOL_DIR%\bd2w.exe"
if not exist "%BD2W_TOOL_DIR%" (
    mkdir "%BD2W_TOOL_DIR%"
    if errorlevel 1 exit /b 1
)
set "BD2W_LOCK=%BD2W_TOOL_DIR%\.bootstrap.lock"
set "BD2W_ATTEMPTS=0"
:lock
mkdir "%BD2W_LOCK%" 2>nul
if errorlevel 1 goto wait
set "BD2W_TEMP=%BD2W_TOOL_DIR%\.bd2w-%RANDOM%-%RANDOM%.exe"
call :compile_tool
if errorlevel 1 goto failed
if not exist "%BD2W_TOOL%" goto replace_tool
fc /b "%BD2W_TEMP%" "%BD2W_TOOL%" >nul 2>nul
if errorlevel 1 goto replace_tool
del /q "%BD2W_TEMP%"
if errorlevel 1 goto failed
goto unlock
:replace_tool
move /y "%BD2W_TEMP%" "%BD2W_TOOL%" >nul
if errorlevel 1 goto failed
:unlock
rmdir "%BD2W_LOCK%"
if errorlevel 1 exit /b 1
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
if defined BD2W_TEMP if exist "%BD2W_TEMP%" del /q "%BD2W_TEMP%"
rmdir "%BD2W_LOCK%"
echo bd2w: Build tool compilation failed. 1>&2
exit /b 1

:compile_tool
setlocal
pushd "%BD2W_ROOT%go"
if errorlevel 1 exit /b 1
set "GOCACHE=%BD2W_ROOT%go\.cache\go-build"
set "GOOS="
set "GOARCH="
for /f "delims=" %%A in ('go env GOHOSTOS') do set "GOOS=%%A"
for /f "delims=" %%A in ('go env GOHOSTARCH') do set "GOARCH=%%A"
if not defined GOOS goto compile_failed
if not defined GOARCH goto compile_failed
set "CGO_ENABLED=0"
go build -buildvcs=false -trimpath -o "%BD2W_TEMP%" ./build
if errorlevel 1 goto compile_failed
popd
exit /b 0
:compile_failed
popd
exit /b 1

:check_csharp
shift
set "BD2W_PROJECTS="
:check_arguments
if "%~1"=="" goto check_start
set "BD2W_PROJECT="
for %%P in (GameSdk GameNames LocalIdentity LoginUI CashShop CaptureEnvironment) do if "%~1"=="%%P" set "BD2W_PROJECT=%%P"
if not defined BD2W_PROJECT (
    echo bd2w: Unknown C# project: "%~1" 1>&2
    exit /b 1
)
set "BD2W_PROJECTS=%BD2W_PROJECTS% %BD2W_PROJECT%"
shift
goto check_arguments
:check_start
if not defined BD2W_PROJECTS set "BD2W_PROJECTS=GameSdk GameNames LocalIdentity LoginUI CashShop CaptureEnvironment"
pushd "%BD2W_ROOT%"
if errorlevel 1 exit /b 1
set "Configuration=Release"
set "BD2W_FAILED=0"
for %%P in (%BD2W_PROJECTS%) do call :check_project %%P
popd
exit /b %BD2W_FAILED%

:check_project
echo Checking %~1
call dotnet build "%BD2W_ROOT%plugins\%~1\%~1.csproj" -c Release -t:Rebuild --nologo -warnaserror
if errorlevel 1 (
    set "BD2W_FAILED=1"
    exit /b 0
)
call dotnet format style "%BD2W_ROOT%plugins\%~1\%~1.csproj" --verify-no-changes --severity info --no-restore
if errorlevel 1 set "BD2W_FAILED=1"
exit /b 0
