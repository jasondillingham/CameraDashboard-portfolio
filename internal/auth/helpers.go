package auth

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// searchUsers performs the LDAP search and returns User objects
func (c *LDAPClient) searchUsers(filter string) ([]*User, error) {
	return c.searchUsersWithOptions(filter, true)
}

// searchUsersBasic performs the LDAP search without populating DirectReports
func (c *LDAPClient) searchUsersBasic(filter string) ([]*User, error) {
	return c.searchUsersWithOptions(filter, false)
}

// searchUsersWithOptions performs the LDAP search with configurable population options
func (c *LDAPClient) searchUsersWithOptions(filter string, includeDirectReports bool) ([]*User, error) {
	searchReq := ldap.NewSearchRequest(
		c.baseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(searchTimeout.Seconds()),
		false,
		filter,
		standardAttributes,
		nil,
	)

	sr, err := c.conn.Search(searchReq)
	if err != nil {
		// Connection may be stale, try to reconnect once
		log.Printf("[WARN] LDAP search failed, attempting reconnect: %v", err)
		if reconnErr := c.reconnect(); reconnErr != nil {
			return nil, fmt.Errorf("LDAP search error: %w", err)
		}
		sr, err = c.conn.Search(searchReq)
		if err != nil {
			return nil, fmt.Errorf("LDAP search error: %w", err)
		}
	}

	users := make([]*User, 0, len(sr.Entries))
	for _, entry := range sr.Entries {
		user, err := c.populateUserFromEntryWithOptions(entry, includeDirectReports)
		if err != nil {
			log.Printf("[WARN] error populating user: %v", err)
			continue
		}
		users = append(users, user)
	}

	return users, nil
}

// populateUserFromEntryWithOptions creates a User with configurable options
func (c *LDAPClient) populateUserFromEntryWithOptions(entry *ldap.Entry, includeDirectReports bool) (*User, error) {
	user := &User{
		DistinguishedName:        entry.GetAttributeValue("distinguishedName"),
		Name:                     entry.GetAttributeValue("cn"),
		DisplayName:              entry.GetAttributeValue("displayName"),
		SamAccountName:           entry.GetAttributeValue("sAMAccountName"),
		UserPrincipalName:        entry.GetAttributeValue("userPrincipalName"),
		Description:              entry.GetAttributeValue("description"),
		GivenName:                entry.GetAttributeValue("givenName"),
		Surname:                  entry.GetAttributeValue("sn"),
		Department:               entry.GetAttributeValue("department"),
		Company:                  entry.GetAttributeValue("company"),
		Title:                    entry.GetAttributeValue("title"),
		Office:                   entry.GetAttributeValue("physicalDeliveryOfficeName"),
		Manager:                  entry.GetAttributeValue("manager"),
		Email:                    entry.GetAttributeValue("mail"),
		TelephoneNumber:          entry.GetAttributeValue("telephoneNumber"),
		Mobile:                   entry.GetAttributeValue("mobile"),
		HomePhone:                entry.GetAttributeValue("homePhone"),
		IPPhone:                  entry.GetAttributeValue("ipPhone"),
		Pager:                    entry.GetAttributeValue("pager"),
		FacsimileTelephoneNumber: entry.GetAttributeValue("facsimileTelephoneNumber"),
		ObjectGUID:               entry.GetAttributeValue("objectGUID"),
	}

	// Handle account control
	if uac := entry.GetAttributeValue("userAccountControl"); uac != "" {
		if val, err := strconv.ParseInt(uac, 10, 32); err == nil {
			user.Enabled = (val & 2) == 0
		}
	}

	// Handle timestamps
	user.AccountExpires = convertADTime(entry.GetAttributeValue("accountExpires"))
	user.LastLogon = convertADTime(entry.GetAttributeValue("lastLogon"))
	user.WhenCreated = convertADTime(entry.GetAttributeValue("whenCreated"))

	// Get group memberships (always needed for authorization)
	if err := c.populateUserGroups(user); err != nil {
		return nil, err
	}

	// DirectReports is expensive - only fetch when explicitly requested
	if includeDirectReports {
		if err := c.populateDirectReports(user); err != nil {
			return nil, err
		}
	}

	return user, nil
}

// populateUserGroups retrieves and sets the user's group memberships
func (c *LDAPClient) populateUserGroups(user *User) error {
	groupSearchBase := c.groupsDN

	// Use LDAP_MATCHING_RULE_IN_CHAIN for nested group membership
	filter := fmt.Sprintf(
		"(&(member:1.2.840.113556.1.4.1941:=%s)(objectClass=group))",
		ldap.EscapeFilter(user.DistinguishedName),
	)

	searchReq := ldap.NewSearchRequest(
		groupSearchBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(searchTimeout.Seconds()),
		false,
		filter,
		[]string{"distinguishedName", "cn"},
		nil,
	)

	sr, err := c.conn.Search(searchReq)
	if err != nil {
		log.Printf("[WARN] LDAP group search failed, attempting reconnect: %v", err)
		if reconnErr := c.reconnect(); reconnErr != nil {
			return fmt.Errorf("LDAP group search error: %w", err)
		}
		sr, err = c.conn.Search(searchReq)
		if err != nil {
			return fmt.Errorf("LDAP group search error: %w", err)
		}
	}

	groups := make([]string, 0, len(sr.Entries)+1)
	groups = append(groups, user.SamAccountName)

	for _, entry := range sr.Entries {
		if cn := entry.GetAttributeValue("cn"); cn != "" {
			groups = append(groups, cn)
		}
	}

	user.MemberOf = groups
	return nil
}

// populateDirectReports retrieves and sets the user's direct reports
func (c *LDAPClient) populateDirectReports(user *User) error {
	filter := fmt.Sprintf(
		"(&(manager=%s)(objectClass=%s))",
		ldap.EscapeFilter(user.DistinguishedName),
		objectClassUser,
	)

	searchReq := ldap.NewSearchRequest(
		c.baseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(searchTimeout.Seconds()),
		false,
		filter,
		[]string{"distinguishedName"},
		nil,
	)

	sr, err := c.conn.Search(searchReq)
	if err != nil {
		log.Printf("[WARN] LDAP direct reports search failed, attempting reconnect: %v", err)
		if reconnErr := c.reconnect(); reconnErr != nil {
			return fmt.Errorf("LDAP direct reports search error: %w", err)
		}
		sr, err = c.conn.Search(searchReq)
		if err != nil {
			return fmt.Errorf("LDAP direct reports search error: %w", err)
		}
	}

	reports := make([]string, 0, len(sr.Entries))
	for _, entry := range sr.Entries {
		if dn := entry.GetAttributeValue("distinguishedName"); dn != "" {
			reports = append(reports, dn)
		}
	}

	user.DirectReports = reports
	return nil
}

// convertADTime converts Active Directory's time format to time.Time
func convertADTime(adTime string) time.Time {
	if adTime == "" {
		return time.Time{}
	}

	// Try parsing as integer (Windows FileTime format)
	if i, err := strconv.ParseInt(adTime, 10, 64); err == nil {
		seconds := i / 10000000
		nanoseconds := (i % 10000000) * 100
		return time.Unix(seconds-11644473600, nanoseconds).UTC()
	}

	// Try various string formats
	formats := []string{
		time.RFC3339,
		"20060102150405.0Z",
		"20060102150405Z",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, adTime); err == nil {
			return t
		}
	}

	log.Printf("[WARN] error parsing AD time: value=%s error=no valid format found", adTime)
	return time.Time{}
}
