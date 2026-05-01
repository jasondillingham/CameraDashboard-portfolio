package auth

import (
	"fmt"
	"log"
	"sync"

	"github.com/go-ldap/ldap/v3"
)

// LDAPClient wraps LDAP connection handling.
type LDAPClient struct {
	host     string
	baseDN   string
	bindDN   string
	bindPass string
	groupsDN string
	conn     *ldap.Conn
	mu       sync.Mutex
}

// LDAPConfig holds LDAP connection settings
type LDAPConfig struct {
	Host     string
	BaseDN   string
	BindDN   string
	BindPass string
	GroupsDN string
}

// NewLDAPClient creates a new LDAP client
func NewLDAPClient(cfg LDAPConfig) *LDAPClient {
	log.Printf("[INFO] auth.NewLDAPClient() host=%s", cfg.Host)
	return &LDAPClient{
		host:     cfg.Host,
		baseDN:   cfg.BaseDN,
		bindDN:   cfg.BindDN,
		bindPass: cfg.BindPass,
		groupsDN: cfg.GroupsDN,
	}
}

// connect establishes an LDAP connection
func (c *LDAPClient) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	conn, err := ldap.DialURL(c.host)
	if err != nil {
		return fmt.Errorf("ldap connection error: %w", err)
	}

	if err := conn.Bind(c.bindDN, c.bindPass); err != nil {
		conn.Close()
		return fmt.Errorf("ldap bind error: %w", err)
	}

	c.conn = conn
	return nil
}

// Close closes the LDAP connection
func (c *LDAPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}
