package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type config struct {
	VideoDir, Host, Port, FFmpegBin                                        string
	Addr, Store, DBPath                                                    string
	AuthMode, AdminUser, AdminPass                                         string
	LDAPAddr, LDAPBaseDN, LDAPBindDN, LDAPBindPassword                     string
	LDAPUserFilter, LDAPUserDN, LDAPAttrEmail, LDAPAttrFirst, LDAPAttrLast string
	LDAPSearchBases                                                        []string
	MaxUploadBytes                                                         int64
	TranscodeWorkers                                                       int
	CookieSecure, LDAPAlwaysUpdate                                         bool
	AuthRequestsPerMinute, UploadTimeoutSeconds, TranscodeTimeoutSeconds   int
}

func getenvBool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func loadConfig() (config, error) {
	c := config{
		VideoDir: getenv("VIDEO_DIR", "videos"), Host: getenv("HOST", "127.0.0.1"),
		Port: getenv("PORT", "8081"), FFmpegBin: os.Getenv("FFMPEG_BIN"),
		Addr: os.Getenv("ADDR"), Store: getenv("STORE", "sqlite"), DBPath: os.Getenv("DB_PATH"),
		AuthMode: getenv("AUTH_MODE", "local"), AdminUser: getenv("ADMIN_USER", "admin"),
		AdminPass: os.Getenv("ADMIN_PASS"), CookieSecure: getenvBool("COOKIE_SECURE", false),
		LDAPAddr:   getenv("LDAP_ADDR", "ldap://localhost:389"),
		LDAPBaseDN: getenv("LDAP_BASE_DN", "dc=example,dc=com"),
		LDAPBindDN: os.Getenv("LDAP_BIND_DN"), LDAPBindPassword: os.Getenv("LDAP_BIND_PASSWORD"),
		LDAPUserFilter:   getenv("LDAP_USER_FILTER", "(uid=%s)"),
		LDAPUserDN:       os.Getenv("LDAP_USER_DN"),
		LDAPAttrEmail:    getenv("LDAP_ATTR_EMAIL", "mail"),
		LDAPAttrFirst:    getenv("LDAP_ATTR_FIRST_NAME", "givenName"),
		LDAPAttrLast:     getenv("LDAP_ATTR_LAST_NAME", "sn"),
		LDAPAlwaysUpdate: getenvBool("LDAP_ALWAYS_UPDATE", true),
	}
	if bases := strings.TrimSpace(os.Getenv("LDAP_SEARCH_BASES")); bases != "" {
		for _, b := range strings.Split(bases, ";") {
			if b = strings.TrimSpace(b); b != "" {
				c.LDAPSearchBases = append(c.LDAPSearchBases, b)
			}
		}
	}
	if c.AuthMode != "local" && c.AuthMode != "ldap" && c.AuthMode != "both" {
		return c, fmt.Errorf("AUTH_MODE must be local, ldap or both")
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
	for _, setting := range []struct {
		name, fallback string
		target         *int
		max            int
	}{
		{"AUTH_REQUESTS_PER_MINUTE", "60", &c.AuthRequestsPerMinute, 3600},
		{"UPLOAD_TIMEOUT_SECONDS", "1800", &c.UploadTimeoutSeconds, 86400},
		{"TRANSCODE_TIMEOUT_SECONDS", "1800", &c.TranscodeTimeoutSeconds, 86400},
	} {
		*setting.target, err = strconv.Atoi(getenv(setting.name, setting.fallback))
		if err != nil || *setting.target < 1 || *setting.target > setting.max {
			return c, fmt.Errorf("%s must be between 1 and %d", setting.name, setting.max)
		}
	}
	return c, nil
}

func (c config) address() string {
	if c.Addr != "" {
		return c.Addr
	}
	return net.JoinHostPort(c.Host, c.Port)
}
