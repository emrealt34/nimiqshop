@echo off
setlocal
cd /d "%~dp0"
set MODE=%~1
if "%MODE%"=="" set MODE=preview
node scripts\run-stack.mjs "%MODE%"
exit /b %ERRORLEVEL%
