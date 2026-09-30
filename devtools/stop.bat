@echo off
REM devtools\stop.bat — kills any lingering nimshop backend/static-server
REM dev servers started by start.bat (best-effort).
setlocal
echo Stopping nimshop-server and Astro/Node dev servers...
taskkill /IM nimshop-server.exe /T /F >nul 2>&1
for /f "tokens=5" %%P in ('netstat -ano ^| findstr ":8085 " ^| findstr LISTENING') do (
  taskkill /PID %%P /T /F >nul 2>&1
)
for /f "tokens=5" %%P in ('netstat -ano ^| findstr ":8084 " ^| findstr LISTENING') do (
  taskkill /PID %%P /T /F >nul 2>&1
)
taskkill /IM cloudflared.exe /T /F >nul 2>&1
REM taskkill /F gives the launcher no chance to run its cleanup, so drop the
REM runtime state here: a leftover public-url.json holds a tunnel URL that is
REM already dead, and the next start.bat must never wire the shop to it.
del /q "%~dp0.runtime\public-url.json" >nul 2>&1
del /q "%~dp0.runtime\config.js" >nul 2>&1
echo Done.
exit /b 0
