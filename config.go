package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type config struct {
	VideoDir, Host, Port, UploadUser, UploadPass, FFmpegBin string
	Addr, Store, DBPath                                     string
	MaxUploadBytes                                          int64
	TranscodeWorkers                                        int
}

func loadConfig() (config, error) {
	c := config{
		VideoDir: getenv("VIDEO_DIR", "videos"), Host: getenv("HOST", "127.0.0.1"),
		Port: getenv("PORT", "8081"), UploadUser: getenv("UPLOAD_USER", "admin"),
		UploadPass: os.Getenv("UPLOAD_PASS"), FFmpegBin: os.Getenv("FFMPEG_BIN"),
		Addr: os.Getenv("ADDR"), Store: getenv("STORE", "sqlite"), DBPath: os.Getenv("DB_PATH"),
	}
	if strings.TrimSpace(c.UploadPass) == "" || strings.TrimSpace(c.UploadPass) == "changeme" {
		return c, fmt.Errorf("set UPLOAD_PASS to a nonempty password other than changeme")
	}
	if strings.Contains(c.UploadUser, ":") {
		return c, fmt.Errorf("UPLOAD_USER cannot contain a colon")
	}
	portText := c.Port
	if c.Addr != "" {
		var err error
		_, portText, err = net.SplitHostPort(c.Addr)
		if err != nil {
			return c, fmt.Errorf("ADDR must be host:port: %w", err)
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return c, fmt.Errorf("PORT must be between 1 and 65535")
	}
	if c.Store != "sqlite" && c.Store != "fs" {
		return c, fmt.Errorf("STORE must be sqlite or fs")
	}
	c.MaxUploadBytes, err = strconv.ParseInt(getenv("MAX_UPLOAD_BYTES", "2147483648"), 10, 64)
	if err != nil || c.MaxUploadBytes <= 0 {
		return c, fmt.Errorf("MAX_UPLOAD_BYTES must be a positive integer")
	}
	c.TranscodeWorkers, err = strconv.Atoi(getenv("TRANSCODE_WORKERS", "1"))
	if err != nil || c.TranscodeWorkers < 1 || c.TranscodeWorkers > 16 {
		return c, fmt.Errorf("TRANSCODE_WORKERS must be between 1 and 16")
	}
	return c, nil
}

func (c config) address() string {
	if c.Addr != "" {
		return c.Addr
	}
	return net.JoinHostPort(c.Host, c.Port)
}
