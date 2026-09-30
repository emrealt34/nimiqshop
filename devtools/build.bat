@echo off
REM devtools\build.bat — force-build backend + frontend, then exit.
setlocal
cd /d "%~dp0\.."
node devtools\devtools.mjs rebuild %*
exit /b %ERRORLEVEL%
