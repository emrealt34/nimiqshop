@echo off
echo Stopping nim.shop...
echo.

REM Close windows opened by "start.bat" by their titles.
taskkill /FI "WINDOWTITLE eq nim.shop - Backend*" /T /F >nul 2>&1
taskkill /FI "WINDOWTITLE eq nim.shop - Frontend*" /T /F >nul 2>&1
taskkill /FI "WINDOWTITLE eq nim.shop - Cloudflare Tunnel*" /T /F >nul 2>&1

REM Fallback: also clean processes listening on 8080/4321.
for /f "tokens=5" %%p in ('netstat -ano ^| findstr ":8080" ^| findstr "LISTENING"') do (
    taskkill /PID %%p /F >nul 2>&1
)
for /f "tokens=5" %%p in ('netstat -ano ^| findstr ":4321" ^| findstr "LISTENING"') do (
    taskkill /PID %%p /F >nul 2>&1
)

REM Close Cloudflare tunnel process (npm cloudflared binary).
taskkill /IM cloudflared.exe /F >nul 2>&1
taskkill /IM cloudflared /F >nul 2>&1

echo Backend, frontend and Cloudflare tunnel stopped.
echo Database (BadgerDB) runs inside backend process; when backend
echo stops it also closes. There is no extra service you need to stop.
echo.
pause
