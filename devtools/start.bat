@echo off
REM devtools\start.bat — one-click launcher: builds everything (Go backend +
REM Astro frontend) if needed, then starts the stack with a Cloudflare quick
REM tunnel. Pass dev/preview/tunnel/build/rebuild/--no-build as the first arg.
setlocal
cd /d "%~dp0\.."
set MODE=%~1
if "%MODE%"=="" set MODE=tunnel
node devtools\devtools.mjs %MODE% %2 %3 %4
exit /b %ERRORLEVEL%
