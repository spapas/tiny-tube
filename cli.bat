@echo off
REM Tiny Tube operator CLI: user administration against VIDEO_DIR/STORE/DB_PATH.
REM Usage: cli.bat users list
REM        cli.bat users set-role --username U --role admin
REM        cli.bat users api-key --username U --name "upload script"
go run ./cmd/cli %*
