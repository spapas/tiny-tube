@echo off
REM Tiny Tube operator CLI: user administration against VIDEO_DIR/STORE/DB_PATH.
REM Usage: cli.bat users list
REM        cli.bat users set-role --username U --role admin
go run ./cmd/cli %*
