@echo off
setlocal DisableDelayedExpansion
set "BD2W_ROOT=%~dp0"
call :validate_task %*
if errorlevel 2 goto show_help
if errorlevel 1 exit /b 1
set "BD2W_TOOL_DIR=%BD2W_ROOT%.build\tools"
if not exist "%BD2W_TOOL_DIR%" (
    mkdir "%BD2W_TOOL_DIR%"
    if errorlevel 1 exit /b 1
)
set "BD2W_TEMP=%BD2W_TOOL_DIR%\.bd2w-%RANDOM%-%RANDOM%.exe"
set "BD2W_INPUTS=%BD2W_TEMP%.inputs"
call :check_inputs
if errorlevel 2 exit /b 1
if not errorlevel 1 goto run_tool
set "BD2W_LOCK=%BD2W_TOOL_DIR%\.bootstrap.lock"
set "BD2W_ATTEMPTS=0"
:lock
mkdir "%BD2W_LOCK%" 2>nul
if errorlevel 1 goto wait
call :check_inputs
if errorlevel 2 goto failed
if not errorlevel 1 goto unlock
call :compile_tool
if errorlevel 1 goto failed
move /y "%BD2W_TEMP%" "%BD2W_TOOL%" >nul
if errorlevel 1 goto failed
:unlock
rmdir "%BD2W_LOCK%"
if errorlevel 1 exit /b 1
:run_tool
"%BD2W_TOOL%" %*
set "BD2W_EXIT_CODE=%errorlevel%"
if exist "%BD2W_TEMP%" del /q "%BD2W_TEMP%"
if exist "%BD2W_INPUTS%" del /q "%BD2W_INPUTS%"
exit /b %BD2W_EXIT_CODE%

:show_help
type "%BD2W_ROOT%go\build\help.txt"
exit /b %errorlevel%

:validate_task
if "%~1"=="--plan" (
    shift
    goto validate_task
)
if "%~1"=="" exit /b 2
if "%~1"=="help" exit /b 2
if "%~1"=="-h" exit /b 2
if "%~1"=="--help" exit /b 2
if /i "%~1"=="-Help" exit /b 2
if not exist "%BD2W_ROOT%go\build\tasks.txt" (
    echo bd2w: Task catalog not found. 1>&2
    exit /b 1
)
for /f "usebackq" %%T in ("%BD2W_ROOT%go\build\tasks.txt") do if "%~1"=="%%T" exit /b 0
echo bd2w: unknown task "%~1"; use --help for available commands 1>&2
exit /b 1

:wait
set /a BD2W_ATTEMPTS+=1 >nul
if %BD2W_ATTEMPTS% geq 120 (
    if exist "%BD2W_INPUTS%" del /q "%BD2W_INPUTS%"
    echo bd2w: Bootstrap lock still held at "%BD2W_LOCK%"; check the first launcher. 1>&2
    exit /b 1
)
ping -n 2 127.0.0.1 >nul
goto lock

:failed
if exist "%BD2W_TEMP%" del /q "%BD2W_TEMP%"
if exist "%BD2W_INPUTS%" del /q "%BD2W_INPUTS%"
rmdir "%BD2W_LOCK%"
echo bd2w: Build tool compilation failed. 1>&2
exit /b 1

:check_inputs
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%BD2W_ROOT%go\build\bootstrap.ps1" -Root "%BD2W_ROOT%." -Candidate "%BD2W_INPUTS%"
set "BD2W_CHECK_EXIT=%errorlevel%"
if exist "%BD2W_INPUTS%" set /p BD2W_TOOL=<"%BD2W_INPUTS%"
exit /b %BD2W_CHECK_EXIT%

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
