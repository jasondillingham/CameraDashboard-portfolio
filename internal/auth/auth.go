// Package auth provides LDAP authentication and user management functionality.
package auth

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Common LDAP search attributes
const (
	KeepAliveInterval = 30 * time.Second
	objectClassUser   = "user"
	searchTimeout     = 30 * time.Second
)

var (
	ErrAuthListEmpty = errors.New("authorization attempted but authList is empty")
	ErrNotAuthorized = errors.New("user not authorized")
	ErrInvalidCreds  = errors.New("user ID and/or password are incorrect")
	ErrMultipleUsers = errors.New("multiple users found")
	ErrNoUserFound   = errors.New("user not found")

	// Standard LDAP attributes to retrieve
	standardAttributes = []string{
		"accountExpires", "cn", "company", "department", "description",
		"displayName", "distinguishedName", "facsimileTelephoneNumber",
		"givenName", "homePhone", "ipPhone", "lastLogon", "mail",
		"manager", "mobile", "objectGUID", "pager", "physicalDeliveryOfficeName",
		"sAMAccountName", "sn", "telephoneNumber", "title",
		"userAccountControl", "userPrincipalName", "whenCreated",
	}
)

// User represents an Active Directory user account
type User struct {
	// Core Attributes
	Description       string
	DisplayName       string
	DistinguishedName string
	GivenName         string
	Name              string
	ObjectGUID        string
	SamAccountName    string
	Surname           string
	UserPrincipalName string

	// Organizational Information
	Department    string
	Company       string
	Title         string
	Office        string
	Manager       string
	DirectReports []string

	// Contact Information
	Email                    string
	TelephoneNumber          string
	Mobile                   string
	HomePhone                string
	IPPhone                  string
	Pager                    string
	FacsimileTelephoneNumber string

	// Account Status and Dates
	Enabled        bool
	AccountExpires time.Time
	LastLogon      time.Time
	WhenCreated    time.Time
	MemberOf       []string
}

// AuthenticateUser authenticates an AD user
func (c *LDAPClient) AuthenticateUser(username, password string) (string, error) {
	log.Printf("[DEBUG] auth.AuthenticateUser() start: username=%s", username)
	if username == "" || password == "" {
		return "", ErrInvalidCreds
	}

	if err := c.connect(); err != nil {
		return "", err
	}

	filter := fmt.Sprintf("(&(|(sAMAccountName=%s)(mail=%s))(objectClass=%s))",
		ldap.EscapeFilter(username), ldap.EscapeFilter(username), objectClassUser)

	searchReq := ldap.NewSearchRequest(
		c.baseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(searchTimeout.Seconds()),
		false,
		filter,
		[]string{"dn", "sAMAccountName"},
		nil,
	)

	sr, err := c.conn.Search(searchReq)
	if err != nil {
		// Connection may be stale, try to reconnect once
		log.Printf("[WARN] LDAP search failed, attempting reconnect: %v", err)
		if reconnErr := c.reconnect(); reconnErr != nil {
			return "", fmt.Errorf("LDAP search error: %w", err)
		}
		// Retry search after reconnect
		sr, err = c.conn.Search(searchReq)
		if err != nil {
			return "", fmt.Errorf("LDAP search error: %w", err)
		}
	}

	switch len(sr.Entries) {
	case 0:
		log.Printf("[WARN] authentication failed: no user found for username=%s", username)
		return "", ErrInvalidCreds
	case 1:
		// Continue with authentication
	default:
		log.Printf("[WARN] authentication failed: multiple users for username=%s", username)
		return "", ErrMultipleUsers
	}

	userDN := sr.Entries[0].DN
	if err := c.conn.Bind(userDN, password); err != nil {
		log.Printf("[WARN] authentication failed: userDN=%s error=%v", userDN, err)
		// Rebind with service account after failed auth attempt
		if rebindErr := c.conn.Bind(c.bindDN, c.bindPass); rebindErr != nil {
			log.Printf("[WARN] failed to rebind with service account: %v", rebindErr)
		}
		return "", ErrInvalidCreds
	}

	// Rebind with service account after successful user authentication
	if err := c.conn.Bind(c.bindDN, c.bindPass); err != nil {
		log.Printf("[WARN] failed to rebind with service account: %v", err)
	}

	log.Printf("[INFO] user authenticated: userDN=%s", userDN)
	return sr.Entries[0].GetAttributeValue("sAMAccountName"), nil
}

// GetUser retrieves a user's information
func (c *LDAPClient) GetUser(userID string) (*User, error) {
	if err := c.connect(); err != nil {
		return nil, err
	}

	filter := fmt.Sprintf("(sAMAccountName=%s)", ldap.EscapeFilter(userID))
	users, err := c.searchUsers(filter)
	if err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, ErrNoUserFound
	}
	if len(users) > 1 {
		return nil, ErrMultipleUsers
	}

	return users[0], nil
}

// User list cache for GetAllUsersBasic
var (
	userCacheMu   sync.RWMutex
	userCache     map[string]*User
	userCacheTime time.Time
	userCacheTTL  = 5 * time.Minute
)

// GetAllUsersBasic retrieves all users WITHOUT DirectReports populated.
// Excludes machine accounts (sAMAccountName ending with $) but includes disabled accounts.
// Results are cached for 5 minutes.
func (c *LDAPClient) GetAllUsersBasic() (map[string]*User, error) {
	userCacheMu.RLock()
	if userCache != nil && time.Since(userCacheTime) < userCacheTTL {
		result := userCache
		userCacheMu.RUnlock()
		return result, nil
	}
	userCacheMu.RUnlock()

	log.Printf("[DEBUG] auth.GetAllUsersBasic() start")
	if err := c.connect(); err != nil {
		return nil, err
	}

	// Filter: user objects excluding machine accounts (names ending with $)
	filter := fmt.Sprintf("(&(objectClass=%s)(!(sAMAccountName=*$)))", objectClassUser)
	users, err := c.searchUsersBasic(filter)
	if err != nil {
		return nil, err
	}

	// Create map with capacity based on slice length
	userMap := make(map[string]*User, len(users))

	// Add each user to the map with SamAccountName as key
	for _, user := range users {
		if user.SamAccountName != "" {
			userMap[user.SamAccountName] = user
		} else {
			log.Printf("[WARN] user found without SamAccountName: distinguishedName=%s", user.DistinguishedName)
		}
	}

	log.Printf("[DEBUG] auth.GetAllUsersBasic() complete: user_count=%d", len(users))

	userCacheMu.Lock()
	userCache = userMap
	userCacheTime = time.Now()
	userCacheMu.Unlock()

	return userMap, nil
}

// AnonymousUser returns a user object representing an unauthenticated user
func AnonymousUser() *User {
	return &User{
		DisplayName:       "Anonymous",
		DistinguishedName: "Anonymous",
		SamAccountName:    "anonymous",
		ObjectGUID:        "",
		MemberOf:          []string{"Anonymous"},
		Enabled:           true,
	}
}

// IsAnonymous checks if a user is the anonymous user
func (u *User) IsAnonymous() bool {
	return u != nil && u.SamAccountName == "anonymous"
}

// KeepAlivePing performs a single ping operation to keep the LDAP connection alive
func (c *LDAPClient) KeepAlivePing() {
	doneCh := make(chan bool)
	errCh := make(chan error)

	go func() {
		err := c.ping()
		if err != nil {
			errCh <- err
		} else {
			doneCh <- true
		}
	}()

	select {
	case err := <-errCh:
		log.Printf("[WARN] LDAP keepalive ping failed: %v", err)
		// Try to reconnect
		reconnectCh := make(chan bool)
		go func() {
			err := c.reconnect()
			if err != nil {
				log.Printf("[ERROR] LDAP reconnection failed: %v", err)
			} else {
				log.Printf("[INFO] LDAP connection reestablished successfully")
			}
			reconnectCh <- true
		}()

		select {
		case <-reconnectCh:
		case <-time.After(3 * time.Second):
			log.Printf("[ERROR] LDAP reconnection attempt timed out")
		}

	case <-doneCh:
	case <-time.After(3 * time.Second):
		log.Printf("[ERROR] LDAP ping operation timed out")
	}
}

// ping performs a lightweight LDAP operation to check connection health
func (c *LDAPClient) ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("connection is nil")
	}

	searchReq := ldap.NewSearchRequest(
		c.baseDN,
		ldap.ScopeBaseObject,
		ldap.NeverDerefAliases,
		0,
		1,
		false,
		"(objectClass=*)",
		[]string{"1.1"},
		nil,
	)

	searchReq.TimeLimit = 5

	_, err := c.conn.Search(searchReq)
	return err
}

// reconnect attempts to establish a new LDAP connection
func (c *LDAPClient) reconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	conn, err := ldap.DialURL(c.host)
	if err != nil {
		return fmt.Errorf("LDAP reconnection error: %w", err)
	}

	if err := conn.Bind(c.bindDN, c.bindPass); err != nil {
		conn.Close()
		return fmt.Errorf("LDAP rebind error: %w", err)
	}

	c.conn = conn
	return nil
}

// IsAuthorized checks if a user is authorized based on group membership
func IsAuthorized(authList, memberOf []string) (bool, error) {
	if len(authList) == 0 {
		return false, ErrAuthListEmpty
	}

	if slices.Contains(memberOf, "Admins") {
		return true, nil
	}

	if slices.ContainsFunc(authList, func(s string) bool {
		return strings.EqualFold(s, "Public")
	}) {
		return true, nil
	}

	for _, group := range memberOf {
		if slices.Contains(authList, group) {
			return true, nil
		}
	}

	return false, ErrNotAuthorized
}
