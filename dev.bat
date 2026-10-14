@echo off
REM Tiny Tube dev loop: rebuilds + restarts the server on save (same Go watcher as SHDE).
REM Usage: dev.bat (run from the repository root). Ctrl+C stops the watcher and server.
go run ./cmd/dev/ %*
