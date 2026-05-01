package models

import "time"

// DashboardPermission represents a single user-dashboard permission grant
type DashboardPermission struct {
	ID            int64     `json:"id"`
	UserID        string    `json:"userId"`
	DashboardSlug string    `json:"dashboardSlug"`
	GrantedBy     string    `json:"grantedBy"`
	GrantedAt     time.Time `json:"grantedAt"`
}

// DashboardSlugInfo describes a dashboard for the permissions UI
type DashboardSlugInfo struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// PermissionCategory groups dashboard slugs by category for the admin UI
type PermissionCategory struct {
	Name       string              `json:"name"`
	Dashboards []DashboardSlugInfo `json:"dashboards"`
}

// UserPermissions holds a user's permission state for the admin UI
type UserPermissions struct {
	UserID      string            `json:"userId"`
	DisplayName string            `json:"displayName"`
	IsAdmin     bool              `json:"isAdmin"`
	Granted     map[string]bool   `json:"granted"` // slug -> granted
}

// PermissionsPageData holds all data for the permissions admin tab
type PermissionsPageData struct {
	Users          []UserPermissions    `json:"users"`
	Categories     []PermissionCategory `json:"categories"`
	CamerasSlug    DashboardSlugInfo    `json:"camerasSlug"`
	DollarCapable  map[string]bool      `json:"dollarCapable"`
	SelectedUser   string               `json:"selectedUser"`
	Tab            string               `json:"tab"`
	LDAPError      string               `json:"ldapError,omitempty"`
}
