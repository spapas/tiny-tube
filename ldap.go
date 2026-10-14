package main

// LDAP authentication for Tiny Tube, following the SHDE model in trimmed
// form: LDAP verifies identity only (bind as the user); roles stay local and
// are granted by an administrator after the first login provisions the row.
// No group/role mapping is performed.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const ldapTimeout = 10 * time.Second

// ldapUser is the verified identity returned by the directory.
type ldapUser struct {
	Username  string
	Email     string
	FirstName string
	LastName  string
}

// directory verifies credentials against LDAP. The interface keeps mode
// routing testable without a live server.
type directory interface {
	authenticate(username, password string) (ldapUser, error)
}

type ldapDirectory struct {
	addr, baseDN, bindDN, bindPassword string
	searchBases                        []string
	userFilter, userDNTemplate         string
	attrEmail, attrFirst, attrLast     string
}

// newDirectory builds the live LDAP client. Tests override it with a fake.
var newDirectory = newLDAPDirectory

func newLDAPDirectory(cfg config) (directory, error) {
	d := &ldapDirectory{
		addr: cfg.LDAPAddr, baseDN: cfg.LDAPBaseDN,
		bindDN: cfg.LDAPBindDN, bindPassword: cfg.LDAPBindPassword,
		searchBases: cfg.LDAPSearchBases, userFilter: cfg.LDAPUserFilter,
		userDNTemplate: cfg.LDAPUserDN,
		attrEmail:      cfg.LDAPAttrEmail, attrFirst: cfg.LDAPAttrFirst, attrLast: cfg.LDAPAttrLast,
	}
	if d.userFilter == "" {
		d.userFilter = "(uid=%s)"
	}
	if d.userDNTemplate == "" {
		d.userDNTemplate = "uid=%s," + d.baseDN
	}
	if d.addr == "" || d.baseDN == "" {
		return nil, fmt.Errorf("LDAP_ADDR and LDAP_BASE_DN are required for LDAP authentication")
	}
	return d, nil
}

func (d *ldapDirectory) directDN(username string) string {
	return strings.ReplaceAll(d.userDNTemplate, "%s", ldap.EscapeFilter(username))
}

func (d *ldapDirectory) authenticate(username, password string) (ldapUser, error) {
	var empty ldapUser
	if username == "" || password == "" {
		return empty, errInvalidCredentials
	}
	ctx, cancel := context.WithTimeout(context.Background(), ldapTimeout)
	defer cancel()
	conn, err := dialLDAP(ctx, d.addr)
	if err != nil {
		return empty, err
	}
	defer conn.Close()
	if len(d.searchBases) == 0 && d.bindDN == "" {
		// Direct bind: the DN derives from the username, no lookup needed.
		if err := conn.Bind(d.directDN(username), password); err != nil {
			return empty, mapLDAPError(err)
		}
		return ldapUser{Username: username}, nil
	}
	if d.bindDN != "" {
		if err := conn.Bind(d.bindDN, d.bindPassword); err != nil {
			return empty, fmt.Errorf("LDAP service bind: %w", err)
		}
	}
	entry, err := d.searchUser(conn, username)
	if err != nil {
		return empty, err
	}
	if err := conn.Bind(entry.DN, password); err != nil {
		return empty, mapLDAPError(err)
	}
	return ldapUser{
		Username:  username,
		Email:     entry.GetAttributeValue(d.attrEmail),
		FirstName: entry.GetAttributeValue(d.attrFirst),
		LastName:  entry.GetAttributeValue(d.attrLast),
	}, nil
}

func (d *ldapDirectory) searchUser(conn *ldap.Conn, username string) (*ldap.Entry, error) {
	bases := d.searchBases
	if len(bases) == 0 {
		bases = []string{d.baseDN}
	}
	filter := strings.ReplaceAll(d.userFilter, "%s", ldap.EscapeFilter(username))
	for _, base := range bases {
		res, err := conn.Search(ldap.NewSearchRequest(
			base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
			0, 0, false, filter,
			[]string{d.attrEmail, d.attrFirst, d.attrLast}, nil,
		))
		if err != nil {
			return nil, fmt.Errorf("LDAP search: %w", err)
		}
		if len(res.Entries) == 1 {
			return res.Entries[0], nil
		}
	}
	// Zero or ambiguous matches stay vague to avoid user enumeration.
	return nil, errInvalidCredentials
}

func mapLDAPError(err error) error {
	if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
		return errInvalidCredentials
	}
	return fmt.Errorf("LDAP bind: %w", err)
}

// dialLDAP opens a deadline-bounded connection. ldaps:// uses TLS 1.2+;
// plain ldap:// is cleartext and suits local/test servers only.
func dialLDAP(ctx context.Context, addr string) (*ldap.Conn, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("LDAP_ADDR: %w", err)
	}
	if u.Scheme != "ldap" && u.Scheme != "ldaps" {
		return nil, fmt.Errorf("LDAP_ADDR must start with ldap:// or ldaps://")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("LDAP_ADDR needs a host")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "ldaps" {
			port = "636"
		} else {
			port = "389"
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(ldapTimeout)
	}
	dialer := &net.Dialer{Timeout: ldapTimeout}
	var raw net.Conn
	if u.Scheme == "ldaps" {
		raw, err = tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port),
			&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		raw, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	}
	if err != nil {
		return nil, fmt.Errorf("LDAP dial: %w", err)
	}
	if err := raw.SetDeadline(deadline); err != nil {
		raw.Close()
		return nil, fmt.Errorf("LDAP deadline: %w", err)
	}
	conn := ldap.NewConn(raw, u.Scheme == "ldaps")
	conn.SetTimeout(ldapTimeout - time.Second)
	conn.Start()
	return conn, nil
}
