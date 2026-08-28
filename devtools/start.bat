@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0\.."

echo ============================================
echo   nim.shop - Starting (Windows + Cloudflare tunnel)
echo ============================================
echo.

REM --- Is Go installed ---
where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go not found. Install from https://go.dev/dl/ and try again.
    pause
    exit /b 1
)

REM --- Is Node installed ---
where node >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Node.js not found. Install from https://nodejs.org and try again.
    pause
    exit /b 1
)

REM --- ENV: no need to fill manually ---
REM    ensure-env.js creates backend/.env from example if missing and auto-fills
REM    required fields (JWT_SECRET, CRYPTOREFILLS_WEBHOOK_KEY, PUBLIC_WEBHOOK_BASE_URL)
REM    -> backend starts WITHOUT ERROR. Your existing values are preserved.
echo [INFO] Preparing backend environment (auto-filling env values)...
pushd "%~dp0"
node ensure-env.js
popd

REM --- Download backend dependencies ---
echo.
echo [INFO] Verifying backend dependencies (go mod download)...
pushd backend
go mod download
if errorlevel 1 (
    echo [ERROR] go mod download failed. Check your internet connection.
    popd
    pause
    exit /b 1
)
popd

REM --- Cloudflared npm dependency (devtools) ---
REM  Checks if binary itself is present (not just folder).
REM  If missing, re-runs npm install (postinstall downloads binary).
REM  If still missing, tunnel-run.js will handle it with its own downloader.
echo.
echo [INFO] Checking cloudflared dependency (npm install in devtools)...
pushd devtools
if not exist "node_modules\cloudflared\bin\cloudflared.exe" if not exist "node_modules\cloudflared\bin\cloudflared" (
    echo [INFO] cloudflared binary missing; re-running npm install...
    call npm install
    if errorlevel 1 (
        echo [WARN] npm install failed. If binary is still
        echo        missing, tunnel-run.js will auto-download it.
    )
)
if not exist "node_modules\cloudflared" call npm install
if errorlevel 1 (
    echo [ERROR] npm install failed. Check your internet connection.
    popd
    pause
    exit /b 1
)
popd

echo [INFO] Starting backend -^> http://localhost:8080
echo         A LIVE LOG window will open (also saved to backend\backend.log).
start "" /b cmd /c "cd /d "%~dp0\..\backend" && go run ./cmd/server > backend.log 2>&1 & pause"
ping -n 2 127.0.0.1 >nul
start "nim.shop backend - LIVE LOG" cmd /c "cd /d "%~dp0\..\backend" && powershell -NoProfile -Command "Get-Content backend.log -Wait -Tail 200""

REM --- Short wait for backend to come up ---
timeout /t 4 /nobreak >nul

echo [INFO] Waiting for backend health (max 30s)...
REM Uses curl.exe (built into Windows 10/11). A failed check is only a
REM WARNING: the backend may still be running - the LIVE LOG window and
REM backend\backend.log always show the truth. Startup NEVER aborts here.
where curl >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
  echo [WARN] curl.exe not found - skipping the health check. Continuing.
  goto healthskipped
)
set /a TRIES=0
:waitbackend
curl -s -o nul --max-time 2 http://localhost:8080/api/health
if %ERRORLEVEL% EQU 0 goto healthok
set /a TRIES+=1
if %TRIES% GEQ 30 (
  echo [WARN] Could not verify backend health from the launcher after 30s.
  echo        The backend may still be running - check the LIVE LOG window
  echo        or backend\backend.log. Continuing startup anyway.
  goto healthskipped
)
timeout /t 1 /nobreak >nul
goto waitbackend
:healthok
echo [INFO] Backend healthy.
:healthskipped

echo [INFO] Starting frontend (static + /api proxy) -^> http://localhost:4321
start "" /b cmd /c "set BACKEND=http://127.0.0.1:8080&& set PORT=4321&& cd /d "%~dp0\..\frontend" && node dev/live-proxy.js"

REM --- Cloudflare quick tunnel (uses npm cloudflared) ---
echo [INFO] Starting Cloudflare tunnel...
echo          This opens a public https://{random}.trycloudflare.com URL.
start "" /b cmd /c "cd /d "%~dp0" && node tunnel-run.js --port 4321"

REM --- Read tunnel URL from file and show in main window ---
set "URL="
for /l %%i in (1,1,20) do (
    if exist "%~dp0tunnel-url.txt" (
        set /p URL=<"%~dp0tunnel-url.txt"
        if not "!URL!"=="" goto :goturl
    )
    timeout /t 1 /nobreak >nul
)

:goturl
echo.
echo ============================================================
echo   Started!
echo   Local  Frontend : http://localhost:4321
echo   Local  Backend  : http://localhost:8080
if not "!URL!"=="" (
    REM tunnel-run.js may write full URL (https://...); don't add https again here.
    set "PUBLIC_URL=!URL!"
    :strip_scheme
    if /I "!PUBLIC_URL:~0,8!"=="https://" set "PUBLIC_URL=!PUBLIC_URL:~8!" & goto strip_scheme
    if /I "!PUBLIC_URL:~0,7!"=="http://" set "PUBLIC_URL=!PUBLIC_URL:~7!" & goto strip_scheme
    echo   PUBLIC  Live URL : https://!PUBLIC_URL!
    echo           (changes on each run; can also be opened on phone)
) else (
    echo   PUBLIC  Live URL : (not yet obtained - use https://*.trycloudflare.com URL
    echo                       in 'nim.shop - Cloudflare Tunnel' window)
)
echo ============================================================
echo.
echo NOTE: Backend was started even without you entering env values (JWT_SECRET /
echo CRYPTOREFILLS_WEBHOOK_KEY / PUBLIC_WEBHOOK_BASE_URL auto-filled).
echo For real CryptoRefills payments you need CRYPTOREFILLS_PARTNER_ID and
echo a STABLE webhook domain (PUBLIC_WEBHOOK_BASE_URL); this tunnel
echo is for DEMO/preview only.
echo.
timeout /t 5 /nobreak >nul
if not "!URL!"=="" start "" "https://!PUBLIC_URL!"
pause
