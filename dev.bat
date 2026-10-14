@echo off
REM Tiny Tube dev loop (cmd.exe): rebuilds + restarts on source, asset or config saves.
REM Usage: dev.bat
REM Ctrl+C stops the watcher. Stop the server with: taskkill /f /im tiny-tube-dev.exe
setlocal
cd /d "%~dp0"
if not exist tmp mkdir tmp
set "EXE=tmp\tiny-tube-dev.exe"
set "NEW=tmp\tiny-tube-dev.exe.new"
set "HASHCUR=tmp\devhash.cur"
set "HASHNEW=tmp\devhash.new"

call :build_new
if errorlevel 1 (echo [dev] initial build failed & exit /b 1)
move /y "%NEW%" "%EXE%" >nul
start "tiny-tube-dev" /min "%EXE%"
echo [dev] watching Go + web assets + config - save to rebuild+restart, Ctrl+C to stop

:loop
timeout /t 2 /nobreak >nul
(for %%f in (*.go go.mod go.sum .env) do @if exist "%%f" for /f "skip=1 delims=" %%h in ('certutil -hashfile "%%f" SHA256 ^| find /v "CertUtil"') do @echo %%h %%f
for /r store %%f in (*.go) do @for /f "skip=1 delims=" %%h in ('certutil -hashfile "%%f" SHA256 ^| find /v "CertUtil"') do @echo %%h %%f
for /r web %%f in (*.html *.css *.js) do @for /f "skip=1 delims=" %%h in ('certutil -hashfile "%%f" SHA256 ^| find /v "CertUtil"') do @echo %%h %%f) > "%HASHNEW%" 2>nul
if not exist "%HASHCUR%" (copy /y "%HASHNEW%" "%HASHCUR%" >nul & goto loop)
fc "%HASHCUR%" "%HASHNEW%" >nul 2>&1
if not errorlevel 1 goto loop
echo [dev] change detected - rebuilding...
call :build_new
if errorlevel 1 (echo [dev] build failed - keeping old process running & goto loop)
taskkill /f /im tiny-tube-dev.exe >nul 2>&1
timeout /t 1 /nobreak >nul
move /y "%NEW%" "%EXE%" >nul
start "tiny-tube-dev" /min "%EXE%"
echo [dev] up
copy /y "%HASHNEW%" "%HASHCUR%" >nul
goto loop

:build_new
go build -o "%NEW%" .
exit /b %errorlevel%
